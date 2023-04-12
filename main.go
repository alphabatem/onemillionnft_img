package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type TxnResponse struct {
	Transaction string `json:"transaction"`
	Message     string `json:"message"`
}

type Job struct {
	x int
	y int
	c color.Color
}

type Worker struct {
	ID int
}

func (w *Worker) Run(imgFill *ImageFill) {
	for job := range imgFill.jobs {
		err := imgFill.paint(job.x, job.y, job.c)
		if err != nil {
			log.Printf("Failed paint: %v,%v: %s", imgFill.startX+job.x, imgFill.startY+job.y, err)
		}
		imgFill.wg.Done()
	}
}

type ImageFill struct {
	wallet *solana.Wallet

	rpc    *rpc.Client
	client *http.Client

	startX int
	startY int

	jobs chan Job
	wg   sync.WaitGroup

	successCalls []string
}

func (i *ImageFill) paint(x, y int, color color.Color) error {
	// Do something with x, y, and color

	tx, err := i.getTxn(i.startX+x, i.startY+y, color)
	if err != nil {
		return err
	}

	if tx == "" {
		return errors.New("invalid txn data")
	}

	sig, err := i.sendTxn(tx)
	if err != nil {
		return err
	}

	return nil
	return i.checkSuccess(i.startX+x, i.startY+y, color, sig)
}

func (i *ImageFill) getTxn(x, y int, color color.Color) (string, error) {
	log.Printf("Painting: %v,%v - #%s", x, y, i.rgbaToHex(color))
	uri := fmt.Sprintf("https://www.onemillionnfts.page/api/mint?x=%v&y=%v&color=%%23%s&pubkey=%s", x, y, i.rgbaToHex(color), i.wallet.PublicKey())

	resp, err := i.client.Get(uri)
	if err != nil {
		return "", err
	}

	if resp.StatusCode == 400 {
		return "", errors.New("already painted")
	}

	if resp.StatusCode != 200 {
		log.Println(uri)
		return "", errors.New(fmt.Sprintf("get txn failed: %v - %s", resp.StatusCode, resp.Status))
	}

	var r TxnResponse

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	err = json.Unmarshal(data, &r)
	if err != nil {
		return "", err
	}

	return r.Transaction, nil
}

func (i *ImageFill) sendTxn(tx string) (solana.Signature, error) {

	data, err := base64.StdEncoding.DecodeString(tx)
	if err != nil {
		return solana.Signature{}, err
	}

	txn, err := solana.TransactionFromDecoder(bin.NewBorshDecoder(data))
	if err != nil {
		return solana.Signature{}, err
	}

	_, err = txn.PartialSign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(i.wallet.PublicKey()) {
			return &i.wallet.PrivateKey
		}
		return nil
	})
	if err != nil {
		return solana.Signature{}, err
	}

	sigs := []solana.Signature{}

	for _, s := range txn.Signatures {
		if s.Equals(solana.Signature{}) {
			continue
		}
		sigs = append([]solana.Signature{s}, sigs...)
	}
	txn.Signatures = sigs

	retries := uint(3)
	sig, err := i.rpc.SendTransactionWithOpts(context.TODO(), txn, rpc.TransactionOpts{
		SkipPreflight: true,
		MaxRetries:    &retries,
	})
	if err != nil {
		//log.Println(txn)
		log.Printf("Txn error: %s", err)
		return solana.Signature{}, err
	}

	return sig, nil
}

func (i *ImageFill) checkSuccess(x, y int, color color.Color, sig solana.Signature) error {
	uri := fmt.Sprintf("https://www.onemillionnfts.page/api/success?x=%v&y=%v&color=%%23%s&transaction=%s&pubkey=%s", x, y, i.rgbaToHex(color), sig, i.wallet.PublicKey())
	i.successCalls = append(i.successCalls, uri)

	return i.doSuccessCheck(uri)
}

func (i *ImageFill) doSuccessCheck(uri string) error {
	log.Println(uri)
	resp, err := i.client.Get(uri)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return errors.New(fmt.Sprintf("check success failed: %v", resp.StatusCode))
	}

	return nil
}

func (i *ImageFill) rgbaToHex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("%02X%02X%02X", r>>8, g>>8, b>>8)
}

func (i *ImageFill) InitWorkerPool(numWorkers int) {
	for id := 1; id <= numWorkers; id++ {
		worker := &Worker{
			ID: id,
		}
		go worker.Run(i)
	}
}

func main() {
	sourceFlag := flag.String("source", "", "The path to the source image file")
	sourceX := flag.String("x", "", "Starting x coordinate")
	sourceY := flag.String("y", "", "Starting y coordinate")
	flag.Parse()

	if *sourceFlag == "" {
		fmt.Println("Please provide a source image using the --source flag")
		os.Exit(1)
	}

	if *sourceX == "" {
		fmt.Println("Please provide a starting x coordinate using the --x flag")
		os.Exit(1)
	}

	if *sourceY == "" {
		fmt.Println("Please provide a starting y coordinate using the --y flag")
		os.Exit(1)
	}

	x, _ := strconv.Atoi(*sourceX)
	y, _ := strconv.Atoi(*sourceY)

	file, err := os.Open(*sourceFlag)
	if err != nil {
		fmt.Println("Error opening image file:", err)
		os.Exit(1)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		fmt.Println("Error decoding image:", err)
		os.Exit(1)
	}

	wallet, err := solana.WalletFromPrivateKeyBase58(os.Getenv("KEYPAIR"))
	if err != nil {
		panic(err)
	}

	skipWhitespace, _ := strconv.ParseBool(os.Getenv("SKIP_WHITESPACE"))

	imgFill := ImageFill{
		wallet:       wallet,
		rpc:          rpc.New(os.Getenv("RPC_URL")),
		client:       &http.Client{Timeout: 60 * time.Second},
		startX:       x,
		startY:       y,
		jobs:         make(chan Job, 100),
		wg:           sync.WaitGroup{},
		successCalls: []string{},
	}

	log.Println("Init worker pool")
	imgFill.InitWorkerPool(30)

	log.Println("Looping through img")
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.At(x, y)

			if skipWhitespace && imgFill.rgbaToHex(c) == "FFFFFF" {
				continue
			}

			imgFill.wg.Add(1)
			imgFill.jobs <- Job{x: x, y: y, c: c}
		}
	}
	close(imgFill.jobs)
	imgFill.wg.Wait()

	log.Println("Done")
}
