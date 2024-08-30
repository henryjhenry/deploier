package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/redis/go-redis/v9"
)

var (
	as        = flag.String("as", "", "as noticer or deploier, options: noticer, deploier")
	svc       = flag.String("service", "", "service name, works on noticer")
	version   = flag.String("version", "", "service docker image version, works on noticer")
	redisAddr = flag.String("redis", "", "the redis server address")
)

func main() {
	flag.Parse()
	if *as == "" || *redisAddr == "" {
		flag.Usage()
		os.Exit(1)
	}
	switch *as {
	case "deploier":
		if err := listen(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	case "noticer":
		if *version == "" || *svc == "" {
			flag.Usage()
			os.Exit(1)
		}
		if err := notice(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		return
	default:
		flag.Usage()
	}
}

func loadRunArgs() (map[string][]string, error) {
	var runArgs map[string][]string
	f, err := os.Open("run_args.json")
	if err != nil {
		return nil, err
	}
	content, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(content, &runArgs); err != nil {
		return nil, err
	}
	return runArgs, nil
}

func pubSubChannel() string {
	return "deploier:deploy"
}

func listen() error {
	cli := redis.NewClient(&redis.Options{
		Addr: *redisAddr,
		DB:   3,
	})
	cmd := cli.Ping(context.Background())
	if err := cmd.Err(); err != nil {
		return err
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	ps := cli.Subscribe(context.Background(), pubSubChannel())
	msgCh := ps.Channel()
	defer func() {
		close(sigCh)
		ps.Close()
		cli.Close()
	}()
	fmt.Println("subscribing...")
	for {
		select {
		case msg := <-msgCh:
			if err := deploy(msg.Payload); err != nil {
				log.Printf("[Error] %v\n", err)
			}
		case sig := <-sigCh:
			return fmt.Errorf("exit cause: %s", sig.String())
		}
	}
}

func deploy(msg string) error {
	args := strings.Split(msg, ":")
	if len(args) < 2 {
		return fmt.Errorf("invalid msg: %s", msg)
	}
	service := args[0]
	image := strings.Join(args[1:], ":")
	cmd := exec.Command("docker", "ps", "-a")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return combineOutputAndErr(output, err)
	}
	lines := strings.Split(string(output), "\n")
	lines = lines[1:]
	var running bool
	for _, line := range lines {
		info := strings.Split(line, " ")
		serviceName := info[len(info)-1]
		if service == serviceName {
			running = true
			break
		}
	}
	if running {
		fmt.Println("stop running service...")
		cmd = exec.Command("docker", "stop", service)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return combineOutputAndErr(output, err)
		}
		fmt.Println(string(output))
		cmd = exec.Command("docker", "rm", service)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return combineOutputAndErr(output, err)
		}
		fmt.Println(string(output))
	}
	fmt.Println("service starting...")
	runArgsAll, err := loadRunArgs()
	if err != nil {
		return fmt.Errorf("load run args: %w", err)
	}
	runArgs, ok := runArgsAll[service]
	if ok {
		runArgs = append(runArgs, "--name", service, image)
		runArgs = slices.Insert(runArgs, 0, "run")
	} else {
		runArgs = []string{"run", "--name", service, image}
	}
	cmd = exec.Command(
		"docker",
		runArgs...,
	)
	fmt.Println(cmd)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return combineOutputAndErr(output, err)
	}
	fmt.Println(string(output))
	fmt.Println("service started")
	return nil
}

func combineOutputAndErr(output []byte, err error) error {
	return fmt.Errorf("%s\n%w", string(output), err)
}

func notice() error {
	cli := redis.NewClient(&redis.Options{
		Addr: *redisAddr,
		DB:   3,
	})
	cmd := cli.Publish(context.Background(), pubSubChannel(), fmt.Sprintf("%s:%s", *svc, *version))
	if err := cmd.Err(); err != nil {
		return err
	}
	return nil
}
