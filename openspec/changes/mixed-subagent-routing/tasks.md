## 1. Effective child model

- [x] 1.1 `ApplyEffectiveChildRunModel` / `ExtractEffectiveRunModelID` in forwarder
- [x] 1.2 Unit tests: explore override local hash, inherit no-op, parent run no-op

## 2. Router + forwarder wiring

- [x] 2.1 AgentRouter classifyBidi: apply + re-encode RequestBody, classify by requested model
- [x] 2.2 encode connect/raw proto helper; strip content-encoding
- [x] 2.3 decodeInboundIntent defense apply
- [x] 2.4 Router tests: official parent child→local; local parent child→upstream

## 3. Verify

- [x] 3.1 `go test` upstream + forwarder packages
