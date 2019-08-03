package main

import (
	"errors"
	"flag"
	"fmt"
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

	err = start_scoring(*name, *email, *class)
	if err != nil {
		fmt.Println("Unable to start scoring:", err)
		os.Exit(1)
	}
	err = score(outer, inner, errchan)
	if err != nil {
		fmt.Println("Unable to complete scoring:", err)
		cancel_scoring()
		os.Exit(1)
	}
	fmt.Println("DONE")
}

func score(outer *tail.Tail, inner *tail.Tail, errchan <-chan error) error {
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
			update_score(outer_buckets, outer_samples, inner_buckets, inner_samples, false)
		case err := <-errchan:
			return err
		}
		if inner_done && outer_done {
			break
		}
	}
	update_score(outer_buckets, outer_samples, inner_buckets, inner_samples, true)
	return nil
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

func start_scoring(name string, email string, class string) error {
	return nil
}

func cancel_scoring() error {
	return nil
}

func update_score(outer_buckets []float32, outer_samples int, inner_buckets []float32, inner_samples int, commit bool) {
	max := float32(-100.0)
	min := float32(100.0)

	fmt.Println(outer_buckets[0], inner_buckets[0])

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
	fmt.Println(min, max)
}
