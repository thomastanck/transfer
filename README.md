# transfer
One-to-one transient transfer service

`transfer` lets people transfer large files to one another without needing p2p or ftp (because those are too hard, am I right?), without storing any files on the server.

The way it does this is by hanging the upload connection until the downloader is connected, and then writing each chunk it receives to the downloader immediately, blocking as necessary. Thankfully this is amazingly easy to do in Go.

You can either follow the on screen instructions at `/` or use the following API (`/` is just an AJAX uploader using this API after all).

# API

## Creating a session

`GET /newsession`

----

Returns a 64 character token that represents the session. This session times out after 60 seconds, but is refreshed to 300 seconds after one of the clients connect.

Example output:
```
HgEvDhBADEEx7uurZK9J9RlGA1d1nxJD5WICsalc-fD4IgFymKzGHU1Lu1SooSGS
```

## Uploading

`PUT /up/:token`

`PUT /up/:token/*unused`

----

Upload your file to this endpoint. If the session doesn't exist or if a previous connection to this endpoint has already been made, the connection will be dropped.

## Downloading

`GET /up/:token`

`GET/up/:token/*unused`

----

Download the file from this endpoint. 
