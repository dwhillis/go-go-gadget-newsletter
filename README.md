# Go-Go-Gadget Newsletter
## Why does this exist
Based off of [Kill the Newsletter](https://github.com/leafac/kill-the-newsletter), this is a go based service that converts emails into an RSS feed.

## How it works
1. Provision a server
2. Set up DNS
You need two records:
* An A record pointing to the server (eg A rss.example.com 1.1.1.1)
* A MX record pointing to the server (eg MX 1 rss.example.com)
3. Run the process on the server. You can use the associated service file.
* Place the file at /etc/systemd/system/go-go-gadget-newsletter.service
* Run the following:
```
# systemctl daemon-reload
# systemctl enable go-go-gadget-newsletter
# systemctl restart go-go-gadget-newsletter
```