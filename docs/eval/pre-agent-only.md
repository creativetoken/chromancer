# Baseline eval — pre-agent-only (deterministic drivers, mock model)

Tag: pre-agent-only · 66e049d · 2026-11-21T01:11Z · backend chromium · provider mock (deterministic flow)

## Auth scenarios (`agentbrowser eval`)

```
SCENARIO                   MODEL                     OK%   avg_ms   steps   tokens   cost_usd
bound-session/login        mock                     100%    15628     4.0        0     0.0000
captcha-mock/login         mock                     100%    15542     4.0        0     0.0000
form-errors/login          mock                     100%    18105     6.0        0     0.0000
hidden-entry/login         mock                     100%    15520     4.0        0     0.0000
iframe/login               mock                     100%    15551     3.0        0     0.0000
modal/login                mock                     100%    16317     4.0        0     0.0000
multistep/login            mock                     100%    17741     5.0        0     0.0000
otp-sms/login              mock                     100%    17804     5.0        0     0.0000
signup-verify/signup       mock                     100%    18501     5.0        0     0.0000
simple/login               mock                     100%    17327     4.0        0     0.0000
spa-jwt/login              mock                     100%    15777     3.0        0     0.0000
totp/login                 mock                     100%    17902     5.0        0     0.0000
```

## Crawl drivers (`agentbrowser eval --crawl`)

```
Crawl drivers vs the lab crawl app:

| driver | states | coverage | forms | denied | steps | tokens | cost_usd | ms | stop |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| bfs | 5/5 | 100% | 1 | 2 | 0 | 0 | 0.0000 | 45822 | frontier_empty |
| llm | 1/5 | 20% | 0 | 0 | 6 | 5805 | 0.0000 | 5894 | plateau |
| hybrid | 5/5 | 100% | 0 | 2 | 5 | 4420 | 0.0000 | 46223 | frontier_empty |
```
