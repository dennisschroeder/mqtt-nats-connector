package cmd

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dennisschroeder/mqtt-nats-connector/internal/logic"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/nats"
	"github.com/spf13/cobra"
)

var (
	natsURL      string
	mqttBroker   string
	clientID     string
	mirrorTopics string
	logLevel     string

	sceneRetryDelays []time.Duration
	sceneRetryMax    int
	sceneRetryWindow time.Duration
)

var rootCmd = &cobra.Command{
	Use:   "mqtt-nats-connector",
	Short: "Mirror MQTT topics to NATS JetStream",
	Run: func(cmd *cobra.Command, args []string) {
		// Setup structured logging
		var level slog.Level
		if err := level.UnmarshalText([]byte(logLevel)); err != nil {
			level = slog.LevelInfo
		}
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
		slog.SetDefault(logger)

		mClient, err := mqtt.NewClient(mqttBroker, clientID)
		if err != nil {
			slog.Error("Failed to connect to MQTT", "error", err)
			os.Exit(1)
		}

		nClient, err := nats.NewClient(natsURL)
		if err != nil {
			slog.Error("Failed to connect to NATS", "error", err)
			os.Exit(1)
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		topics := strings.Split(mirrorTopics, ",")
		svc := logic.NewService(mClient, nClient, topics, logic.WithSceneRetry(logic.SceneRetryConfig{
			Delays:     sceneRetryDelays,
			MaxRetries: sceneRetryMax,
			Window:     sceneRetryWindow,
		}))

		// Run disconnects MQTT itself during shutdown; NATS goes last because
		// queued MQTT messages are still published to it.
		err = svc.Run(ctx)
		nClient.Close()
		if err != nil {
			slog.Error("Service failed", "error", err)
			os.Exit(1)
		}
		slog.Info("Shutdown complete")
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&natsURL, "nats-url", "nats://nats.event-bus:4222", "NATS URL")
	rootCmd.PersistentFlags().StringVar(&mqttBroker, "mqtt-broker", "tcp://mosquitto.iot:1883", "MQTT Broker")
	rootCmd.PersistentFlags().StringVar(&clientID, "client-id", "mqtt-nats-connector", "Client ID")
	rootCmd.PersistentFlags().StringVar(&mirrorTopics, "topics", "presence/#,waste/#", "Comma-separated topics to mirror")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")

	retry := logic.DefaultSceneRetryConfig()
	rootCmd.PersistentFlags().DurationSliceVar(&sceneRetryDelays, "scene-retry-delays", retry.Delays, "Delays before each Zigbee scene recall retry after z2m reports a transient radio error; the last one repeats")
	rootCmd.PersistentFlags().IntVar(&sceneRetryMax, "scene-retry-max", retry.MaxRetries, "Max retries of a failed Zigbee scene recall (0 disables)")
	rootCmd.PersistentFlags().DurationVar(&sceneRetryWindow, "scene-retry-window", retry.Window, "How long after a Zigbee scene recall a reported failure still triggers a retry")
}
