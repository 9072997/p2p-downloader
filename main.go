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
import "os"
import "errors"

type partIdentifier struct {
	location string
	number uint
}

// 10 MB
const partSize = 10485760

const secondsAfterLastPeer = 10
var secondsToStayUp uint = 0

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

var parts map[partIdentifier][]byte
var partsMutex sync.Mutex
func init() {
	partsMutex.Lock()
	parts = make(map[partIdentifier][]byte)
	partsMutex.Unlock()
}

func main() {
	go serveData()
	go serveMetadata()
	
	packageLocation := os.Args[1]
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
	fmt.Println()
}

// this assumes all partes of the package are already downloaded
func writePackageData(packageName string, dest io.Writer) error {
	partsMutex.Lock()
	defer partsMutex.Unlock()
	
	for partNumber := uint(0); true; partNumber++ {
		partID := partIdentifier {
			location: packageName,
			number: partNumber,
		}
		
		if _, exists := parts[partID]; exists {
			// next part in sequence exists, write it out
			_, err := dest.Write(parts[partID])
			if err != nil {
				return err
			}
		} else {
			// next part in sequence does not exist, were done
			return nil
		}
	}
	
	return errors.New("You got out of an infinite loob with no break?")
}

func getPackage(packageLocation string) error {
	// get number of parts
	response, err := http.Head("http://" + packageLocation)
	if err != nil {
		return err
	}
	contentLength := response.ContentLength
	numberOfParts := int(contentLength / partSize)
	fmt.Println("There are", numberOfParts, "parts to", packageLocation)
	
	// make a "deck" of part numbers
	// we are only using the key part of a map for this
	partsToGet := make(map[uint]struct{})
	for i := 0; i < numberOfParts; i++ {
		partsToGet[uint(i)] = struct{}{}
	}
	
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
		}
	}
	
	fmt.Println("Finished with", packageLocation)
	
	return nil
}

func getPartFromPeer(peerAddress string, name string, partNumber uint) error {
	uri := fmt.Sprintf("http://%s/%s/%d", peerAddress, name, partNumber)
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
	
	partID := partIdentifier {
		location: name,
		number: partNumber,
	}
	
	// save data to our global parts map
	partsMutex.Lock()
	parts[partID] = response
	partsMutex.Unlock()
	
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
	
	partID := partIdentifier {
		location: name,
		number: partNumber,
	}
	
	// save data to our global parts map
	partsMutex.Lock()
	parts[partID] = response
	partsMutex.Unlock()
	
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
		response := buf[0:length]
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
		partNumberStrings := strings.Split(string(response), ",")
		var partNumbers []uint
		for _, partNumberString := range partNumberStrings {
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

func serveData() {
	//setup handler
	http.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		// someone used us as a peer, reset the countdown
		secondsToStayUp = secondsAfterLastPeer
		
		matchParts := regexp.MustCompile("^/(.*)/([0-9]+)$").FindStringSubmatch(request.RequestURI)
		if len(matchParts) != 3 {
			// bad request URI
			response.WriteHeader(http.StatusBadRequest)
			io.WriteString(response, "Use the path format /example.com/foo.pkg/7 for part 7 of foo.pkg\n")
			return
		}
		
		partNumber, err := strconv.ParseUint(matchParts[2], 10, 32)
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		
		partID := partIdentifier {
			location: matchParts[1],
			number: uint(partNumber),
		}
		
		partsMutex.Lock()
		if data, exists := parts[partID]; exists {
			partsMutex.Unlock()
			
			// send the data from the selected part
			response.Header().Set("Content-Type", "application/octet-stream")
			response.Write(data)
		} else {
			partsMutex.Unlock()
			
			response.WriteHeader(http.StatusNotFound)
			io.WriteString(response, "Requested part is not avalible on this node (or may not exist)\n")
		}
	})
	// start serving
	http.ListenAndServe(":1817", nil)
}

func serveMetadata() {
	// any address at port 1817
	listenPort, err := net.ResolveUDPAddr("udp",":1817")
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
		
		// loop over locally avalible parts and see if we have anything
		var avaliblePartNumbers[] uint
		partsMutex.Lock()
		for partID := range parts {
			if partID.location == desiredPackage {
				avaliblePartNumbers = append(avaliblePartNumbers, partID.number)
			}
		}
		partsMutex.Unlock()
		
		// if we have anything avalible
		if len(avaliblePartNumbers) > 0 {
			// make a csv line of the parts we have
			csv := ""
			for _, partNumber := range avaliblePartNumbers {
				csv += fmt.Sprint(partNumber)
				csv += ","
			}
			csv = strings.TrimSuffix(csv, ",")
			
			// send that back as a udp response
			//LOGfmt.Printf("Sending response '%s' to %v\n", csv, clientAddr)
			socket.WriteToUDP([]byte(csv), clientAddr)
		}
	}
}
