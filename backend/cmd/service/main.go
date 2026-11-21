package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"flowline/internal/api/http"
	"flowline/internal/cli/process"
	"flowline/internal/client/github"
	"flowline/internal/config/app"
	"flowline/internal/repository/sqlite"
	"flowline/internal/service/run"
)

func main() {
	if err := serve(); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(process.Redact(err.Error()))
	}
}

func serve() error {
	config, err := app.Load(os.Args[1:])
	if err != nil {
		return err
	}

	absoluteData := config.DataDir
	if err := os.MkdirAll(absoluteData, 0700); err != nil {
		return err
	}

	lock, err := os.OpenFile(filepath.Join(absoluteData, "server.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("эта база уже открыта другим экземпляром Flowline")
	}

	store, err := sqlite.OpenStore(filepath.Join(absoluteData, "flowline.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.RecoverRuns(); err != nil {
		return err
	}

	engine := run.NewEngine(store, absoluteData, config.SettingsPath, nil, github.New())
	defer engine.Close()
	if err := engine.InitializeExecutors(config.Executors); err != nil {
		return err
	}
	server := &http.Server{
		Addr:              config.Listen,
		Handler:           httpapi.NewServer(store, engine, github.New(), config.FrontendDir),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       time.Minute,
	}
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()

	stopping, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverError := make(chan error, 1)
	go func() { serverError <- server.Serve(listener) }()
	fmt.Printf("Flowline: http://%s\nНастройки: %s\nДанные: %s\n", listener.Addr(), config.SettingsPath, absoluteData)

	select {
	case <-stopping.Done():
		engine.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	case err := <-serverError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return err
	}
}
