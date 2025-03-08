# diagrams

Temporary file for updated diagrams from the [README](./README.md) done natively in mermaid to allow version-controlled updates.

```mermaid
flowchart LR
  subgraph Unbounded
    direction LR
    subgraph http-proxy
      egress
    end
    subgraph blocked-peer
      direction LR
      client
    end
    subgraph unblocked-peer
      direction BT
      widget <==> egress
    end
    subgraph matchmaking
      freddie <--> widget
    end
  end
  client <==> |proxy| widget
  egress <==> internet
  client <--> freddie
```
