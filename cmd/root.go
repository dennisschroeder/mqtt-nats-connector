package cmd

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/dennisschroeder/mqtt-nats-connector/internal/logic"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/nats"
	"github.com/spf13/cobra"
)

var (
	natsURL    string
	mqttBroker string
	clientID   string
	mirrorTopics string
)

var rootCmd = &cobra.Command{
	Use:   "mqtt-nats-connector",
	Short: "Mirror MQTT topics to NATS JetStream",
	Run: func(cmd *cobra.Command, args []string) {
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

		topics := strings.Split(mirrorTopics, ",")
		svc := logic.NewService(mClient, nClient, topics)
		
		if err := svc.Run(context.Background()); err != nil {
			slog.Error("Service failed", "error", err)
			os.Exit(1)
		}
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
}
