# gopitd in Docker

Short answer: yes, but it manages the **host** best from the host. In a
container it can only see what you share with it.

Build:

```bash
docker build -f Dockerfile.gopitd -t gopitd:dev .
```

Run (keeps node identity, manages host containers via the socket):

```bash
docker run -d --name gopitd --restart unless-stopped \
  -p 1221:1221/tcp -p 1221:1221/udp \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v gopitd-etc:/etc/gopitd \
  -v gopitd-data:/var/lib/gopitd \
  gopitd:dev -config /etc/gopitd/gopitd.yaml
```

Or point at a remote daemon without a socket mount:

```bash
-e DOCKER_HOST=tcp://host.docker.internal:2375
```

What works / what degrades:

| Feature | In container |
|---|---|
| `docker.*` | Works with the socket mounted or `DOCKER_HOST` set; else clean "docker unavailable" error, no crash |
| `system.*` stats | Reports the **container**, not the host (needs host `/proc` to be truthful — out of scope) |
| Firewall (`nftfw`/`ufw`) | Container netns only, useless for the host; needs `--network host --privileged` + host netlink, which defeats the isolation |
| Terminal | Containers usually run as root → refused (`ErrAgentRoot`); needs host users/`su` anyway |
| UDP discovery | Needs published ports; LAN broadcast won't cross a bridge — add the node manually by IP |

Recommendation: run `gopitd` on the host via `install.sh agent`. Use the
container only for `docker.*` management of a socket-shared host.
