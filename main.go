package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hpcloud/tail"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [args] outer_sweep_file inner_sweep_file\n", os.Args[0])
		flag.PrintDefaults()
	}
	url := flag.String("u", "", "URL of TFH scoreboard system")
	key := flag.String("k", "", "API key for TFH scoreboard")
	name := flag.String("n", "", "Name of contestant being scored")
	email := flag.String("e", "", "Email of competitor")
	class := flag.String("c", "", "Competition class e.g. unlimited")
	flag.Parse()

	if len(*url) == 0 || len(*key) == 0 {
		fmt.Println("-u and -k required")
		os.Exit(1)
	}

	if flag.NArg() != 2 {
		fmt.Println("outer and inner sweep files required")
		os.Exit(1)
	}

	conf := tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: true,
	}

	outer, err := tail.TailFile(flag.Arg(0), conf)
	if err != nil {
		fmt.Printf("Cannot open %s: %v", flag.Arg(0), err)
		os.Exit(1)
	}
	inner, err := tail.TailFile(flag.Arg(1), conf)
	if err != nil {
		fmt.Printf("Cannot open %s: %v", flag.Arg(1), err)
		os.Exit(1)
	}

	// USR1 tells the scorer that no more data is being written and to finish
	// up and send the final score

	errchan := make(chan error, 1)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGUSR1)

	go func() {
		sig := <-sigs
		if sig == syscall.SIGUSR1 {
			fmt.Println("Finishing scoring...")
			outer.StopAtEOF()
			inner.StopAtEOF()
		} else {
			fmt.Println("Canceling scoring...")
			errchan <- errors.New("Scoring canceled")
		}
	}()

	id, err := start_scoring(*url, *key, *name, *email, *class)
	_ = id
	if err != nil {
		fmt.Println("Unable to start scoring:", err)
		os.Exit(1)
	}
	err = score(outer, inner, errchan, *url, *key, id)
	if err != nil {
		fmt.Println("Unable to complete scoring:", err)
		cancel_scoring(*url, *key, id)
		os.Exit(1)
	}
	fmt.Println("DONE")
}

func score(outer *tail.Tail, inner *tail.Tail, errchan <-chan error, url string, key string, id string) error {
	outer_samples := 0
	inner_samples := 0
	outer_buckets := []float32{}
	inner_buckets := []float32{}
	outer_done := false
	inner_done := false
	ticker := time.NewTicker(1 * time.Second)
	for {
		select {
		case ln := <-outer.Lines:
			if ln == nil {
				if !outer_done {
					fmt.Println("Parsing outer sweep file complete")
					outer_done = true
				}
				break
			}
			if ln.Err != nil {
				return ln.Err
			} else {
				outer_samples++
				outer_buckets = process_sample(ln.Text, outer_buckets)
			}
		case ln := <-inner.Lines:
			if ln == nil {
				if !inner_done {
					fmt.Println("Parsing inner sweep file complete")
					inner_done = true
				}
				break
			}
			if ln.Err != nil {
				return ln.Err
			} else {
				inner_samples++
				inner_buckets = process_sample(ln.Text, inner_buckets)
			}
		case <-ticker.C:
			update_score(outer_buckets, outer_samples, inner_buckets, inner_samples, url, key, id, false)
		case err := <-errchan:
			return err
		}
		if inner_done && outer_done {
			break
		}
	}
	return update_score(outer_buckets, outer_samples, inner_buckets, inner_samples, url, key, id, true)
}

func process_sample(line string, buckets []float32) []float32 {
	parts := strings.Split(line, ",")
	// not enough data
	if len(parts) < 7 {
		return buckets
	}
	parts = parts[6:]
	for i := len(buckets); i < len(parts); i++ {
		buckets = append(buckets, 0.0)
	}
	for i, v := range parts {
		f, err := strconv.ParseFloat(strings.Trim(v, " "), 32)
		if err == nil {
			buckets[i] += float32(f)
		}
	}

	return buckets
}

func start_scoring(url string, key string, name string, email string, class string) (string, error) {
	type CreateScoreRequest struct {
		Name  string  `json:"name"`
		Class string  `json:"class"`
		Email *string `json:"email",omitempty`
	}
	type CreateScoreResponse struct {
		ID string `json:"id"`
	}

	req := &CreateScoreRequest{
		Name:  name,
		Class: class,
	}
	if len(email) != 0 {
		req.Email = &email
	}
	resp := &CreateScoreResponse{}

	err := do_request(url+"/admin/score", key, "POST", req, resp)
	if err != nil {
		return "", err
	}

	fmt.Printf("Scoring started for %s in %s\n", name, class)
	fmt.Printf("Score ID: %s\n", resp.ID)
	return resp.ID, nil
}

func cancel_scoring(url string, key string, id string) error {
	fmt.Printf("Canceling scoring for %s\n", id)
	return do_request(url+"/admin/score/"+id, key, "DELETE", nil, nil)
}

func update_score(outer_buckets []float32, outer_samples int, inner_buckets []float32, inner_samples int, url string, key string, id string, commit bool) error {
	max := float32(-100.0)
	min := float32(100.0)

	for i := range outer_buckets {
		if i < len(inner_buckets) {
			v := (outer_buckets[i] / float32(outer_samples)) - (inner_buckets[i] / float32(inner_samples))
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
	}

	type UpdateScoreRequest struct {
		Score    float32 `json:"score"`
		Finalize bool    `json:"finalize"`
	}
	upd := &UpdateScoreRequest{
		Score:    min + max,
		Finalize: commit,
	}
	fmt.Printf("Current Score: %f\n", upd.Score)
	return do_request(url+"/admin/score/"+id, key, "POST", upd, nil)
}

func do_request(url string, key string, method string, in interface{}, out interface{}) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	resp_body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("Communication error: %s (%s)", resp.Status, string(resp_body))
	}

	if out != nil {
		return json.Unmarshal(resp_body, out)
	}
	return nil
}
