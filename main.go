package main

import "github.com/9072997/jgh"
import "fmt"
import "io"
import "io/ioutil"
import "net"
import "net/http"
import "strings"
import "sync"
import "regexp"
import "strconv"
import "time"
import "os/exec"
import "os"
import "errors"
import "path/filepath"

// 10 MB
const partSize = 10485760

// the minimum number of parts that must be in a download before we
// launch the progress bar
const minNumPartsForProgress = 5

// this is for safety when using the proxy
// Empty string to disable
const serverPrefix = "bsweb01.bentonville.k12.ar.us/jamf"

const secondsAfterLastPeer = 600
var secondsToStayUpMutex sync.Mutex
var secondsToStayUp uint = 0

// seconds between prune attepts
const partsPruneInterval = 10

// if we are managed by launchd, we can take the extreem aproach to
// freeing memory and just die. Launchd will restart us
const dieToFree = true

// maximum message length for UDP packet
// now that we use (0)(1)(2) insted of 0,1,2 this shouldn't be an issue
// NOTE: messages may be a fiew bytes longer than this, because I'm lazy
const maxUdpSize = 99999

// End of config stuff

// technically this should have a mutex, but the safety isn't worth it
var globalStatus = struct {
	name string
	numParts uint
	partsComplete uint
} {
	"Nothing is being downloaded",
	0,
	0,
}

var parts SliverStore
func init() {
	var err error
	parts, err = MakeSliverStore()
	jgh.PanicOnErr(err)
}

func statusNewPackage(packageName string, numParts uint) {
	matchParts := regexp.MustCompile(`([^/.]+)(?:\.[^/]*)?/?$`).FindStringSubmatch(packageName)
	globalStatus.name = matchParts[1]
	globalStatus.numParts = numParts
	globalStatus.partsComplete = 0
	
	if numParts >= minNumPartsForProgress {
		// get path to self (progress launcher should be in the same dir)
		dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
		if err != nil {
			fmt.Println("Error finding path to self:", err)
		}
		
		// launch progress indicator
		err = exec.Command(dir + "/launchAsCurrentUser").Start()
		if err != nil {
			fmt.Println("Error launching progress indicator:", err)
		}
	}
}
func statusPartFinished() {
	if globalStatus.partsComplete < globalStatus.numParts {
		globalStatus.partsComplete++
	}
}

// stolen from stackoverflow 23558425
// GetLocalIP returns the non loopback local IP of the host
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addrs {
		// check the address type and if it is not a loopback the display it
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}

func main() {
	go serveData()
	go serveMetadata()
	
	// wait forever
	select{}
	
	// this was code for a CLI version
	/*packageLocation := os.Args[1]
	filename := os.Args[2]
	
	outputFile, err := os.Create(filename)
	jgh.PanicOnErr(err)
	defer outputFile.Close()
	
	err = getPackage(packageLocation)
	jgh.PanicOnErr(err)
	
	err = writePackageData(packageLocation, outputFile)
	jgh.PanicOnErr(err)
	
	for secondsToStayUp > 0 {
		fmt.Printf("\rStaying up for %02d more second(s) waiting on peers", secondsToStayUp)
		secondsToStayUp--
		time.Sleep(time.Second)
	}
	fmt.Println()*/
}

// this assumes all partes of the package are already downloaded
// this is a legacy wraper function
func writePackageData(packageName string, dest io.Writer) error {
	return parts.GetSlivers(packageName, dest)
}

func getPackage(packageLocation string) (httpStatus int, err error) {
	// get number of parts
	response, err := http.Head("http://" + packageLocation)
	if err != nil {
		return 0, err
	}
	if response.StatusCode != 200 {
		fmt.Println("Remote server returned HTTP status", response.StatusCode)
		return response.StatusCode, errors.New("Remote server did not return 200")
	}
	contentLength := response.ContentLength
	numberOfParts := uint(contentLength / partSize)
	if contentLength % partSize > 0 {
		// there is a small piece at the end
		numberOfParts++
	}
	fmt.Println("There are", numberOfParts, "parts to", packageLocation)
	
	// this is for progress. We will decriment it for parts we have locally
	numPartsToFetch := numberOfParts
	
	// make a "deck" of part numbers
	// we are only using the key part of a map for this
	partsToGet := make(map[uint]struct{})
		for partNumber := uint(0); partNumber < numberOfParts; partNumber++ {
			if parts.Exists(packageLocation, partNumber) {
				// one less part to fetch
				numPartsToFetch--
			} else {
				// we don't already have this part in memory
				// add it to out to-get list
				partsToGet[partNumber] = struct{}{}
			}
		}
	
	// show the progress bar
	statusNewPackage(packageLocation, numPartsToFetch)
	
	downloadLoop: for len(partsToGet) > 0 {
		// try to get stuff from peers
		responses, err := whoHas(packageLocation, 100)
		if err == nil {
			// iterate over peers
			for address, partNumbers := range responses {
				// iterate over parts that peer has
				for _, partNumber := range partNumbers {
					// is that part in our "to do" deck
					if _, exists := partsToGet[partNumber]; exists {
						// get it
						err := getPartFromPeer(address, packageLocation, partNumber)
						if err == nil {
							// we just downloaded a new part, mark it done
							delete(partsToGet, partNumber)
							
							// update status page
							statusPartFinished()
							
							// check with peers again
							continue downloadLoop
						}
					}
				}
			}
		}
		
		// get stuff from normal http server
		var partNumber uint
		for partNumber, _ = range partsToGet {break}
		err = getPartFromLocation(packageLocation, partNumber, uint64(contentLength))
		if err == nil {
			// we downloaded something, mark it off
			delete(partsToGet, partNumber)
			
			// update status page
			statusPartFinished()
		}
	}
	
	fmt.Println("Finished with", packageLocation)
	
	return 200, nil
}

func getPartFromPeer(peerAddress string, name string, partNumber uint) error {
	uri := fmt.Sprintf("http://%s/parts/%s/%d", peerAddress, name, partNumber)
	fmt.Println("Downloading part from", uri)
	responseObj, err := http.Get(uri)
	if err != nil {
		return err
	}
	defer responseObj.Body.Close()
	
	response, err := ioutil.ReadAll(responseObj.Body)
	if err != nil {
		return err
	}
	
	// save data to our global parts map
	parts.Save(name, partNumber, response)
	
	//LOGfmt.Println("Finished downloading part", partNumber)
	
	return nil
}

func getPartFromLocation(name string, partNumber uint, maxSize uint64) error {
	uri := fmt.Sprintf("http://%s", name)
	fmt.Println("Downloading part", partNumber, "from", uri)
	
	request, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		return err
	}
	start := partNumber*partSize
	end := (uint64(partNumber)+1)*uint64(partSize) - 1
	if end > maxSize {
		end = maxSize
	}
	rangeStr := fmt.Sprintf("bytes=%d-%d", start, end)
	request.Header.Add("Range", rangeStr)
	
	var client http.Client
	responseObj, err := client.Do(request)
	if err != nil {
		return err
	}
	defer responseObj.Body.Close()
	
	response, err := ioutil.ReadAll(responseObj.Body)
	if err != nil {
		return err
	}
	
	// save data to our global parts map
	parts.Save(name, partNumber, response)
	
	//LOGfmt.Println("Finished downloading part", partNumber)
	
	return nil
}

// returns only the first response for now
func whoHas(name string, timeoutMs uint) (responses map[string][]uint, err error) {
	//LOGfmt.Printf("Sending WhoHas for %s\n", name)
	
	sourcePort, err := net.ResolveUDPAddr("udp", getLocalIP()+":1818")
	if err != nil {
		return // err is set
	}
	
	destPort, err := net.ResolveUDPAddr("udp", "255.255.255.255:1817")
	if err != nil {
		return // err is set
	}
	
	txSocket, err := net.DialUDP("udp", sourcePort, destPort)
	if err != nil {
		return // err is set
	}
	_, err = txSocket.Write([]byte(name))
	if err != nil {
		return // err is set
	}
	txSocket.Close()
	
	// wait for responses (TODO race condition here)
	rxSocket, err := net.ListenUDP("udp", sourcePort)
	if err != nil {
		return // err is set
	}
	defer rxSocket.Close()
	rxSocket.SetReadDeadline(time.Now().Add(time.Duration(timeoutMs) * time.Millisecond))
	
	// each iteration is a response
	responses = make(map[string][]uint)
	for {
		buf := make([]byte, 1024)
		//LOGfmt.Println("Waiting for response")
		length, serverAddr, err := rxSocket.ReadFromUDP(buf)
		message := buf[0:length]
		if err != nil && err.(net.Error).Timeout() {
			err = nil
			break
		}
		if err != nil {
			fmt.Println("Error while reading response:", err)
			return responses, err
		}
		//LOGfmt.Printf("Got response '%s' from %v\n", response, serverAddr)
		
		// parse response
		partNumberStrings := regexp.MustCompile(`\([0-9]+\)`).FindAllString(string(message), -1)
		var partNumbers []uint
		for _, partNumberString := range partNumberStrings {
			partNumberString = strings.Trim(partNumberString, "()")
			var partNumber uint64
			partNumber, err = strconv.ParseUint(partNumberString, 10, 32)
			if err != nil {
				fmt.Println("Error while parseing response:", err)
				return responses, err
			}
			partNumbers = append(partNumbers, uint(partNumber))
		}
		
		responses[serverAddr.String()] = partNumbers
	}
	
	return
}

// reset the timer for peer activity
func peerActivity() {
	secondsToStayUpMutex.Lock()
	secondsToStayUp = secondsAfterLastPeer
	secondsToStayUpMutex.Unlock()
}

func partsHandler(response http.ResponseWriter, request *http.Request) {
	matchParts := regexp.MustCompile("^/parts/(.*)/([0-9]+)$").FindStringSubmatch(request.RequestURI)
	if len(matchParts) != 3 {
		// bad request URI
		response.WriteHeader(http.StatusBadRequest)
		io.WriteString(response, "Use the path format /parts/example.com/foo.pkg/7 for part 7 of foo.pkg\n")
		return
	}
	
	partNumber, err := strconv.ParseUint(matchParts[2], 10, 32)
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		return
	}
	
	location := matchParts[1]
	data, err := parts.Get(location, uint(partNumber))
	if err == nil {
		// send the data from the selected part
		response.Header().Set("Content-Type", "application/octet-stream")
		response.Write(data)
	} else {
		response.WriteHeader(http.StatusNotFound)
		io.WriteString(response, "Requested part is not avalible on this node (or may not exist)\n")
	}
	
	// reset inactivity timer
	go peerActivity()
}

func proxyHandler(response http.ResponseWriter, request *http.Request) {
	matcher := regexp.MustCompile("^/proxy/(" + regexp.QuoteMeta(serverPrefix) + ".*)$")
	matchParts := matcher.FindStringSubmatch(request.RequestURI)
	if len(matchParts) != 2 {
		response.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(response, "Did not satisfy prefix requirement (%s)\n", serverPrefix)
		fmt.Printf("Did not satisfy prefix requirement: %s\nprefix is %s\n", request.RequestURI, serverPrefix)
		return
	}
	packageLocation := matchParts[1]
	
	// download package
	httpStatus, err := getPackage(packageLocation)
	if err != nil {
		if httpStatus == 0 {
			// 0 means we dodn't get a response on the HEAD request
			response.WriteHeader(http.StatusInternalServerError)
		} else {
			// pass through status from remote server
			response.WriteHeader(httpStatus)
		}
		fmt.Fprintln(response, err)
		fmt.Println(err)
		return
	}
	
	// write it out in order
	response.Header().Set("Content-Type", "application/octet-stream")
	err = writePackageData(packageLocation, response)
	if err != nil {
		fmt.Println(err)
		return
	}
}

func serveData() {
	//setup handlers
	http.HandleFunc("/parts/", partsHandler)
	http.HandleFunc("/proxy/", proxyHandler)
	http.HandleFunc("/status/name", func(response http.ResponseWriter, request *http.Request) {
		fmt.Fprintln(response, globalStatus.name)
	})
	http.HandleFunc("/status/numParts", func(response http.ResponseWriter, request *http.Request) {
		fmt.Fprintln(response, globalStatus.numParts)
	})
	http.HandleFunc("/status/partsComplete", func(response http.ResponseWriter, request *http.Request) {
		fmt.Fprintln(response, globalStatus.partsComplete)
	})
	// start serving
	http.ListenAndServe(":1817", nil)
}

func serveMetadata() {
	// any address at port 1817
	listenPort, err := net.ResolveUDPAddr("udp", ":1817")
	jgh.PanicOnErr(err)
	
	// Now listen at selected port
	socket, err := net.ListenUDP("udp", listenPort)
	jgh.PanicOnErr(err)
	defer socket.Close()
	
	buf := make([]byte, 1024)
	for {
		length, clientAddr, err := socket.ReadFromUDP(buf)
		if err != nil {
			fmt.Println("Error while reading packet: ", err)
			continue
		}
		
		desiredPackage := string(buf[0:length])
		//LOGfmt.Println("Got request for", desiredPackage)
		
		avaliblePartNumbers := parts.List(desiredPackage)
		
		// if we have anything avalible
		if len(avaliblePartNumbers) > 0 {
			// make a line of the parts we have
			message := ""
			for _, partNumber := range avaliblePartNumbers {
				// rather than handle multipacket messages
				// just send a random subset of what we have
				if len(message) >= maxUdpSize {
					break
				}
				message += fmt.Sprintf("(%d)", partNumber)
			}
			
			// send that back as a udp response
			//LOGfmt.Printf("Sending response '%s' to %v\n", csv, clientAddr)
			socket.WriteToUDP([]byte(message), clientAddr)
		}
	}
}
