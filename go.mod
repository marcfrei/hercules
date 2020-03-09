module hercules

go 1.11

require (
	github.com/google/gopacket v1.1.17
	github.com/inconshreveable/log15 v0.0.0-20161013181240-944cbfb97b44
	github.com/scionproto/scion v0.3.1
	github.com/vishvananda/netlink v1.0.0
)

replace github.com/scionproto/scion => github.com/netsec-ethz/scion v0.0.0-20191126100951-b45109524c36