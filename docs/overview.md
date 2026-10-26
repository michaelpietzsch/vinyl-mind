# Overview

How the parts fit together. The details are in the other documents; this page is the map.

## Who talks to whom

```mermaid
flowchart LR
    subgraph stations["Stations (vinyl-check)"]
        S1["Station A<br/>profiles, local models,<br/>own Ed25519 key"]
        S2["Station B"]
    end
    subgraph host["Hub host"]
        P["Reverse proxy<br/>TLS, HSTS, /v1/* only"]
        H["Vinyl-Mind<br/>no outgoing connections"]
        D[("Database<br/>+ server key")]
    end
    O["Operator<br/>command line"]

    S1 -- "HTTPS, signed requests" --> P
    S2 -- "HTTPS, signed requests" --> P
    P -- "HTTP, internal network" --> H
    H --- D
    O -- "vinyl-mind commands" --> H
```

- Stations always open the connection; the hub never reaches into a station's network.
- Every request is signed by the station's key, every model by the server key (`spec/signing.md`). TLS at the
  proxy protects the connection on top.
- The hub needs no way to the internet. Everything that changes it (invitations, approving, revoking, publishing,
  withdrawing) is a command on the host, not an endpoint.

## From joining to using a model

```mermaid
sequenceDiagram
    autonumber
    participant St as Station
    participant Hub as Vinyl-Mind
    participant Op as Operator

    Op->>Hub: vinyl-mind invite
    Note over Op,St: the code reaches the station's user out of band
    St->>Hub: GET /v1/server
    Hub-->>St: server key and fingerprint
    Note over St: user compares the fingerprint, station stores the key
    St->>Hub: POST /v1/join (public key, invitation)
    Hub-->>St: active

    loop after each retraining, while no recording runs
        St->>Hub: PUT /v1/profiles/{id} (weights, counts, self-check)
    end
    Note over Hub: combine within a band around the operator's anchors, by a trimmed mean
    Hub->>Hub: new candidate, signed with the server key

    St->>Hub: GET /v1/models (If-None-Match)
    Hub-->>St: published model and candidate (JWS)
    Note over St: verify the signature, then estimate own master recordings with local and global model
    St->>Hub: PUT /v1/profiles/{id}/checks/{version} (errors)
    Note over Hub: the anchors decide, after the review window - published if not worse, withdrawn if worse
    Note over St: adopt as prior only if not worse on its own recordings
```

## The life of a global model

```mermaid
stateDiagram-v2
    [*] --> candidate: combine, result changed
    candidate --> published: anchors not worse, after the review window
    candidate --> withdrawn: anchors worse, or superseded after the review window
    published --> withdrawn: anchors worse, or the operator withdraws
    candidate --> published: the operator publishes
    withdrawn --> [*]
```

A published model stays published when a newer one is published; stations take the one published last. When
that one is withdrawn, nothing takes its place until the operator publishes a model: stations fall back to their
own.

## Where to read on

| Question | Document |
|---|---|
| What exactly does a station send and receive? | [api.md](api.md), [spec/](../spec/README.md) |
| How are contributions combined, and why that way? | [aggregation.md](aggregation.md) |
| What protects the models and the stations? | [security.md](security.md) |
| What is stored, and for how long? | [privacy.md](privacy.md) |
| How do I run a hub? | [operations.md](operations.md) |
| What does vinyl-check need to take part? | [station-side.md](station-side.md) |
