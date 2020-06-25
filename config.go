// Copyright 2019 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"fmt"
	log "github.com/inconshreveable/log15"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/snet"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	localAddrRegexp = regexp.MustCompile(`^([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}):([0-9]{1,5})$`)
	configurableInterfaceRegexp = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
)

// receiver related

func (config *HerculesReceiverConfig) initializeDefaults() {
	config.HerculesGeneralConfig = HerculesGeneralConfig{
		Direction:    "",
		DumpInterval: 1 * time.Second,
		Interface:    "",
		Mode:         "",
		MTU: 		  1500,
		Queues:       []int{0},
		Verbosity:    "",
	}
	config.OutputFile = ""
	config.LocalAddresses = SiteConfig{}
	config.ConfigureQueues = false
}

// Validates configuration parameters that have been provided, does not validate for presence of mandatory arguments.
func (config *HerculesReceiverConfig) validateLoose() error {
	if config.Direction != "" && config.Direction != "download" {
		return errors.New("field Direction must either be empty or 'download'")
	}
	err, iface := config.HerculesGeneralConfig.validateLoose()
	if err != nil {
		return err
	}

	// check if output file exists (or folder)
	if config.OutputFile != "" {
		if stat, err := os.Stat(config.OutputFile); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else if stat.IsDir() {
			return fmt.Errorf("output file %s is a directory", config.OutputFile)
		} else {
			log.Info(fmt.Sprintf("output file %s exists: will be overwritten", config.OutputFile))
		}
		dir := filepath.Dir(config.OutputFile)
		stat, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if !stat.IsDir() {
			return fmt.Errorf("not a directory: %s", dir)
		}
	}

	// check LocalAddresses
	if len(config.LocalAddresses.HostAddrs) != 0 {
		if (config.LocalAddresses.IA == addr.IA{}) {
			return errors.New("invalid IA")
		}

		for _, address := range config.LocalAddresses.HostAddrs {
			udpAddress, err := parseLocalAddr(address)
			if err != nil {
				return err
			}
			if iface != nil {
				if err := checkAssignedIP(iface, udpAddress.IP); err != nil {
					return err
				}
			}
		}

		if len(config.Queues) != len(config.LocalAddresses.HostAddrs) {
			log.Warn("you should specify exactly one queue for each receiving address to have a separate receiving thread for each address")
		}
	}

	if config.ConfigureQueues {
		if !configurableInterfaceRegexp.MatchString(config.Interface) {
			return fmt.Errorf("cannot configure interface '%s' - escaping not implemented", config.Interface)
		}
	}
	return nil
}

// Validates all configuration parameters, also checks presence of mandatory parameters.
func (config *HerculesReceiverConfig) validateStrict() error {
	if err := config.HerculesGeneralConfig.validateStrict(); err != nil {
		return err
	}
	if err := config.validateLoose(); err != nil {
		return err
	}

	if len(config.LocalAddresses.HostAddrs) == 0 {
		return errors.New("no local addresses given")
	}
	if config.OutputFile == "" {
		return errors.New("no output file specified")
	}

	if config.ConfigureQueues {
		numQueues := len(config.Queues)
		numAddrs := len(config.LocalAddresses.HostAddrs)
		if numQueues > numAddrs {
			log.Warn(fmt.Sprintf("can not use all queues: %d queues and %d addresses given", numQueues, numAddrs))
		} else if numAddrs%numQueues != 0 {
			log.Warn(fmt.Sprintf("can not distribute flows evenly across queues: number of queues (%d) does not divide number of addresses (%d)", numQueues, numAddrs))
		}
	}
	return nil
}

// Merge commandline arguments into the current configuration.
func (config *HerculesReceiverConfig) mergeFlags(flags *Flags) error {
	if err := forbidFlags([]string{"pcc", "p", "d", "t", "np", "be", "resv"}, "receiving"); err != nil {
		return err
	}
	if isFlagPassed("n") {
		config.DumpInterval = flags.dumpInterval * time.Second
	}
	if isFlagPassed("i") {
		config.Interface = flags.ifname
	}
	if isFlagPassed("l") {
		config.LocalAddresses = SiteConfig{}
		for _, localAddr := range flags.localAddrs {
			match := localAddrRegexp.FindStringSubmatch(localAddr)
			if match != nil { // if IP (-l ...:...): add host address
				if (config.LocalAddresses.IA == addr.IA{}) {
					return errors.New("first local address must specify IA")
				}
				config.LocalAddresses.HostAddrs = append(config.LocalAddresses.HostAddrs, localAddr)
			} else { // else, if full SCION address: add host address and set IA
				local, err := snet.ParseUDPAddr(localAddr)
				if err != nil {
					return err
				}
				if local.Host.Port == 0 {
					return errors.New("you must specify a source port")
				}
				if (config.LocalAddresses.IA != addr.IA{}) {
					if config.LocalAddresses.IA != local.IA {
						return errors.New("local addresses must belong to the same AS")
					}
				} else {
					config.LocalAddresses.IA = local.IA
				}
				config.LocalAddresses.HostAddrs = append(config.LocalAddresses.HostAddrs, local.Host.IP.String()+":"+strconv.Itoa(local.Host.Port))
			}
		}
	}
	if isFlagPassed("m") {
		config.Mode = flags.mode
	}
	if isFlagPassed("q") {
		var err error
		err, config.Queues = parseQueues(flags)
		if err != nil {
			return err
		}
	}
	if isFlagPassed("o") {
		config.OutputFile = flags.outputFilename
	}
	if isFlagPassed("v") {
		config.Verbosity = flags.verbose
	}
	if isFlagPassed("mtu") {
		config.MTU = flags.mtu
	}
	return nil
}

// Converts config.LocalAddrtesses into []*net.UDPAddr for use by herculesRx.
// Assumes config (strictly) is valid.
func (config *HerculesReceiverConfig) localAddresses() []*net.UDPAddr {
	var addrs []*net.UDPAddr
	for _, address := range config.LocalAddresses.HostAddrs {
		udpAddress, _ := parseLocalAddr(address) // since config is valid, there can be no error here
		addrs = append(addrs, udpAddress)
	}
	return addrs
}

// sender related

func (config *HerculesSenderConfig) initializeDefaults() {
	config.HerculesGeneralConfig = HerculesGeneralConfig{
		Direction:    "",
		DumpInterval: 1 * time.Second,
		Interface:    "",
		Mode:         "",
		MTU: 		  1500,
		Queues:       []int{0},
		Verbosity:    "",
	}
	config.TransmitFile = ""
	config.EnableReservations = false
	config.EnableBestEffort = true
	config.EnablePCC = true
	config.RateLimit = 3333333
	config.LocalAddress = ""
	config.NumPathsPerDest = 1
	config.Destinations = nil
}

// Validates configuration parameters that have been provided, does not validate for presence of mandatory arguments.
func (config *HerculesSenderConfig) validateLoose() error {
	if config.Direction != "" && config.Direction != "upload" {
		return errors.New("field Direction must either be empty or 'upload'")
	}
	err, iface := config.HerculesGeneralConfig.validateLoose()
	if err != nil {
		return err
	}

	// check that the file exists
	if config.TransmitFile != "" {
		stat, err := os.Stat(config.TransmitFile)
		if err != nil {
			return err
		}
		if stat.IsDir() {
			return errors.New("file to transmit is a directory")
		}
	}

	if config.RateLimit < 100 {
		log.Warn(fmt.Sprintf("rate limit is really low (%d packets per second)", config.RateLimit))
	}

	if config.LocalAddress != "" {
		udpAddr, err := snet.ParseUDPAddr(config.LocalAddress)
		if err != nil {
			return err
		}
		if err := checkAssignedIP(iface, udpAddr.Host.IP); err != nil {
			return err
		}
	}

	if config.NumPathsPerDest > maxPathsPerReceiver {
		return fmt.Errorf("can use at most %d paths per destination; configured limit (%d) too large", maxPathsPerReceiver, config.NumPathsPerDest)
	}

	// validate destinations
	for d, _ := range config.Destinations {
		if (config.Destinations[d].IA == addr.IA{}) {
			return errors.New("invalid IA")
		}

		if config.Destinations[d].NumPaths > maxPathsPerReceiver {
			return fmt.Errorf("can use at most %d paths per destination; max for destination %d is too large (%d)", maxPathsPerReceiver, d, config.Destinations[d].NumPaths)
		}

		for _, address := range config.Destinations[d].HostAddrs {
			_, err := parseLocalAddr(address)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Validates all configuration parameters, checks presence of mandatory parameters and ensures that at least one of
// best-effort or SIBRA is enabled.
func (config *HerculesSenderConfig) validateStrict() error {
	if err := config.HerculesGeneralConfig.validateStrict(); err != nil {
		return err
	}
	if err := config.validateLoose(); err != nil {
		return err
	}

	if config.TransmitFile == "" {
		return errors.New("you must specify a file to send")
	}

	if !config.EnableBestEffort && !config.EnableReservations {
		return errors.New("best-effort traffic and COLIBRI bandwidth reservations both disabled, don't know how to send data")
	}

	if config.LocalAddress == "" {
		return errors.New("you must specify a local address")
	}

	if len(config.Destinations) == 0 {
		return errors.New("you must specify at least one destination")
	}

	for d, _ := range config.Destinations {
		if len(config.Destinations[d].HostAddrs) == 0 {
			return errors.New("you must specify at least one address per destination")
		}
	}
	return nil
}

// Merge commandline arguments into the current configuration.
func (config *HerculesSenderConfig) mergeFlags(flags *Flags) error {
	if err := forbidFlags([]string{"o"}, "sending"); err != nil {
		return err
	}
	if isFlagPassed("n") {
		config.DumpInterval = flags.dumpInterval * time.Second
	}
	if isFlagPassed("pcc") {
		config.EnablePCC = flags.enablePCC
	}
	if isFlagPassed("i") {
		config.Interface = flags.ifname
	}
	if isFlagPassed("l") {
		if len(flags.localAddrs) == 1 {
			config.LocalAddress = flags.localAddrs[0]
		} else {
			config.LocalAddress = ""
		}
	}
	if isFlagPassed("p") {
		config.RateLimit = flags.maxRateLimit
	}
	if isFlagPassed("m") {
		config.Mode = flags.mode
	}
	if isFlagPassed("q") {
		var err error
		err, config.Queues = parseQueues(flags)
		if err != nil {
			return err
		}
	}
	if isFlagPassed("d") {
		sites := make([]SiteConfig, 0)
		siteIdx := -1
		for _, remoteAddr := range flags.remoteAddrs {
			match := localAddrRegexp.FindStringSubmatch(remoteAddr)
			if match != nil { // if IP (-d ...:...): add to previous destination
				if siteIdx == -1 {
					return errors.New("cannot add IP address to destination: no previous destination")
				}
				sites[siteIdx].HostAddrs = append(sites[siteIdx].HostAddrs, remoteAddr)
			} else { // else, if full SCION address: add new destination
				remote, err := snet.ParseUDPAddr(remoteAddr)
				if err != nil {
					return err
				}
				if remote.Host.Port == 0 {
					return errors.New("you must specify a destination port")
				}
				sites = append(sites, SiteConfig{
					IA:        remote.IA,
					HostAddrs: []string{remote.Host.IP.String() + ":" + strconv.Itoa(remote.Host.Port)},
				})
				siteIdx++
			}
		}
		config.Destinations = sites
	}
	if isFlagPassed("t") {
		config.TransmitFile = flags.transmitFilename
	}
	if isFlagPassed("v") {
		config.Verbosity = flags.verbose
	}
	if isFlagPassed("np") {
		config.NumPathsPerDest = flags.numPaths
	}
	if isFlagPassed("be") {
		config.EnableBestEffort = flags.enableBestEffort
	}
	if isFlagPassed("resv") {
		config.EnableReservations = flags.enableSibra
	}
	if isFlagPassed("mtu") {
		config.MTU = flags.mtu
	}
	return nil
}

// Converts config.Destinations into []*Destination for use by herculesTx.
// Assumes config (strictly) is valid.
func (config *HerculesSenderConfig) destinations() []*Destination {
	var dests []*Destination
	for d, _ := range config.Destinations {
		dest := &Destination{
			ia:        config.Destinations[d].IA,
			hostAddrs: []*net.UDPAddr{},
			pathSpec:  &config.Destinations[d].PathSpec,
			numPaths:  config.NumPathsPerDest,
		}
		if config.Destinations[d].NumPaths > 0 {
			dest.numPaths = config.Destinations[d].NumPaths
		}
		dests = append(dests, dest)

		for _, address := range config.Destinations[d].HostAddrs {
			udpAddr, _ := parseLocalAddr(address) // since config is valid, there can be no error here
			dest.hostAddrs = append(dest.hostAddrs, udpAddr)
		}
	}
	return dests
}

// helpers

func (config *HerculesGeneralConfig) validateLoose() (error, *net.Interface) {
	var iface *net.Interface
	if config.Direction != "" && config.Direction != "upload" && config.Direction != "download" {
		return errors.New("field Direction must either be 'upload', 'download' or empty"), nil
	}
	if config.DumpInterval <= 0 {
		return errors.New("field DumpInterval must be strictly positive"), nil
	}
	if config.Interface != "" {
		var err error
		iface, err = net.InterfaceByName(config.Interface)
		if err != nil {
			return err, nil
		}
		if iface.Flags&net.FlagUp == 0 {
			return errors.New("interface is not up"), nil
		}
	}
	if config.Mode != "z" && config.Mode != "c" && config.Mode != "" {
		return fmt.Errorf("unknown mode %s", config.Mode), nil
	}

	if config.MTU < minFrameSize {
		return fmt.Errorf("MTU too small: %d < %d", config.MTU, minFrameSize), nil
	}
	if config.MTU > 9038 {
		return fmt.Errorf("can not use jumbo frames of size %d > 9038", config.MTU), nil
	}

	sort.Ints(config.Queues)
	for i, q := range config.Queues {
		if q < 0 {
			return errors.New("queue number must be positive"), nil
		}
		if i > 0 && config.Queues[i-1] == q {
			return fmt.Errorf("can use a queue only once, queue %d passed multiple times", q), nil
		}
	}

	if config.Verbosity != "" && config.Verbosity != "v" && config.Verbosity != "vv" {
		return errors.New("verbosity must be empty or one of 'v', 'vv'"), nil
	}
	return nil, iface
}

// Check that the mandatory general configuration has been set.
//
// WARNING: this function does not validate the contents of the options to avoid duplicate calls to validateLoose(),
// as this function is called within Hercules(Sender|Receiver)Config.validateLoose() already.
func (config *HerculesGeneralConfig) validateStrict() error {
	if config.Interface == "" {
		return errors.New("you must specify a network interface to use")
	}
	if len(config.Queues) == 0 {
		return errors.New("you must specify at least one queue")
	}
	if config.MTU > 8015 {
		log.Warn(fmt.Sprintf("using frame size %d > 8015 (IEEE 802.11)", config.MTU))
	}
	return nil
}

func (config *HerculesGeneralConfig) getXDPMode() (mode int) {
	switch config.Mode {
	case "z":
		mode = XDP_ZEROCOPY
	case "c":
		mode = XDP_COPY
	default:
		mode = 0
	}
	return mode
}

// Checks that none of flags are passed by the command line.
// mode should either be "sending" or "receiving" and is only used in errors
//
// Returns an error if any of the provided flags was passed by the command line, nil otherwise
func forbidFlags(flags []string, mode string) error {
	var illegalFlags []string
	for _, f := range flags {
		if isFlagPassed(f) {
			illegalFlags = append(illegalFlags, f)
		}
	}

	if len(illegalFlags) > 0 {
		return fmt.Errorf("-%s not permitted for %s", strings.Join(illegalFlags, ", -"), mode)
	} else {
		return nil
	}
}

func parseQueues(flags *Flags) (error, []int) {
	queues := make([]int, 0, len(flags.queueArgs))
	for _, qs := range flags.queueArgs {
		q, err := strconv.ParseInt(qs, 10, 32)
		if err != nil {
			return errors.New("could not parse queue: " + err.Error()), nil
		}
		queues = append(queues, int(q))
	}
	return nil, queues
}

func parseLocalAddr(rawHost string) (*net.UDPAddr, error) {
	rawIP, rawPort, err := net.SplitHostPort(rawHost)
	if err != nil {
		return nil, fmt.Errorf("in '%s':"+err.Error(), rawHost)
	}
	ip := net.ParseIP(rawIP)
	if ip == nil {
		return nil, fmt.Errorf("in '%s': invalid address: no IP specified", rawHost)
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("in '%s': invalid port", rawHost)
	}
	return &net.UDPAddr{IP: ip, Port: int(port)}, nil
}

func checkAssignedIP(iface *net.Interface, localAddr net.IP) (err error) {
	// Determine src IP matches information on Interface
	interfaceAddrs, err := iface.Addrs()
	if err != nil {
		return
	}
	for _, ifAddr := range interfaceAddrs {
		ip, ok := ifAddr.(*net.IPNet)
		if ok && ip.IP.To4() != nil && ip.IP.To4().Equal(localAddr) {
			return nil
		}
	}
	return fmt.Errorf("interface '%s' does not have the IP address '%s'", iface.Name, localAddr)
}