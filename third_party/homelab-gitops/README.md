# Homelab GitOps Repository

This repository acts as the "Single Source of Truth" for the Kubernetes-based homelab, fully automated and managed via **FluxCD**.

## Architecture

The homelab consists of a 3-node Talos Linux Kubernetes Cluster. The architecture follows a strict "Agent-First" approach to maximize machine-readability, automation, and high availability.

### Core Components

- **OS:** Talos Linux
- **Storage:** Longhorn (distributed block storage)
- **Network & CNI:** Cilium (eBPF)
- **Routing & Ingress:** Kubernetes Gateway API (v1) with Traefik Controller
- **GitOps:** FluxCD (automated deployments from this repository)

### Smart Home (Home Assistant)

- **Database:** Dedicated PostgreSQL instance within the cluster instead of SQLite for maximum performance (configured via `recorder`).
- **IoT Stack:** Eclipse Mosquitto, Zigbee2MQTT, Z-Wave JS UI running as independent microservices in namespaces `iot` and `mqtt`.
- **Heat Pump Integration:** Stiebel Eltron Modbus to MQTT bridge running as a highly optimized, stateless Go microservice in the `iot` namespace, pulling its image from a private GHCR registry.
- **Zero-Downtime Updates:** For major updates of stateful apps like Home Assistant, a "Clone & Test" strategy is used to prevent putting the live system at risk.

### Backup Strategy

- A **Longhorn Recurring Job** creates automatic volume-level backups.
- Backups are encrypted and mirrored to an external Hetzner Storage Box via SFTP through an internal **S3-Gateway** (`rclone` in namespace `backup-system`) to ensure offsite disaster recovery.

## Directory Structure

- `clusters/homelab/`: Contains Flux Kustomizations that declare which directories should be applied to the cluster.
- `infrastructure/`: Foundational services such as network configuration (Gateway API Routes, mDNS Publisher) and backup resources.
- `apps/`: The actual workloads, categorized by domain (e.g., `homeassistant`, `iot`, `mqtt`).

## Local DNS

Since the Gateway API is not yet natively supported by all mDNS reflectors, a custom Python-based publisher (`gateway-mdns`) runs inside the cluster to broadcast the local `.local` domains (e.g., `ha.local`) into the home network.
