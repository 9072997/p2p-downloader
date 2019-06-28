package main

import "golang.org/x/crypto/sha3"
import "fmt"
import "encoding/hex"
import "io/ioutil"
import "github.com/9072997/jgh"
import "os"
import "strings"
import "strconv"
import "sort"
import "io"

type SliverStore struct {
	folder string // must have trailing slash
}

func MakeSliverStore() (s SliverStore, err error) {
	tempDir, err := ioutil.TempDir("", "sliverstore")
	if err != nil {
		err = fmt.Errorf("Can't create temp dir for SliverStore: %v", err)
		return
	}
	
	s = SliverStore {
		folder: tempDir + "/",
	}
	return s, nil
}

func (s SliverStore) Close() error {
	return os.RemoveAll(s.folder)
}

// save a sliver to a sliver store
func (s SliverStore) Save(source string, number uint, data []byte) error {
	id := sliverID(source, number)
	filename := s.folder + id
	err := atomicWriteFile(filename, data)
	if err != nil {return err}
	
	return nil
}

// get's the data in a sliver
func (s SliverStore) Get(source string, number uint) (data []byte, err error) {
	id := sliverID(source, number)
	data, err = ioutil.ReadFile(s.folder + id)
	if err != nil {
		if os.IsNotExist(err) {
			err = fmt.Errorf("SliverStore dosen't contain a sliver '%s:%d'", source, number)
			return
		} else {
			// err already set
			return
		}
	}
	
	return data, nil
}

// check if we already have a particular sliver
func (s SliverStore) Exists(source string, number uint) bool {
	id := sliverID(source, number)
	// "does X exist?" === "can we stat X?"
	_, err := os.Stat(s.folder + id)
	return err == nil
}

// list all slivers we have for a source
func (s SliverStore) List(source string) (results []uint) {
	// if we get an error here this whole object is bunk
	files, err := ioutil.ReadDir(s.folder)
	jgh.PanicOnErr(err)
	
	// we are searching for files ending in needleHash
	needleHash := sourceHash(source)
	for _, file := range files {
		// check if this sliver is from the given source
		filenameParts := strings.Split(file.Name(), "-")
		if len(filenameParts) != 2 {
			continue // weird file, ignore it
		}
		hash := filenameParts[1]
		if hash != needleHash {
			continue // not the hash we are looking for
		}
		number, err := strconv.ParseInt(filenameParts[0], 10, 32)
		if err != nil || number < 0 {
			continue // weird file, ignore it
		}
		results = append(results, uint(number))
	}
	return
}

// writes out all slivers for source to w in order
// this will throw an error if you are missing a sliver in the middle,
// but it can't tell if you are missing one at the end
func (s SliverStore) GetSlivers(source string, w io.Writer) error {
	numbers := s.List(source)
	
	// check that we don't have slivers missing in the middle
	sort.Slice(numbers, func(a, b int) bool {
		return numbers[a] < numbers[b]
	})
	for k, id := range numbers {
		if k != int(id) {
			return fmt.Errorf("Missing sliver %d", k)
		}
	}
	
	// write out slivers
	for _, number := range numbers {
		data, err := s.Get(source, number)
		if err != nil {return err}
		_, err = w.Write(data)
		if err != nil {return err}
	}
	
	return nil
}

// write a file to a temp location, then mv it to the final location
func atomicWriteFile(filename string, data []byte) error {
	f, err := ioutil.TempFile("", "atomicWrite")
	if err != nil {return err}
	
	_, err = f.Write(data)
	if err != nil {return err}
	
	err = f.Close()
	if err != nil {return err}
	
	err = os.Rename(f.Name(), filename)
	if err != nil {return err}
	
	err = os.Remove(f.Name())
	if err != nil {return err}
	
	return nil
}

// generate a sliver ID
// which is partnumber-hashofsource
// base 64 encoded
func sliverID(source string, number uint) string {
	return fmt.Sprintf("%d-%s", number, sourceHash(source))
}

// generate hex sha3-hash of a string
// we use this for hashing sources so they are a known length
// and don't have weird chars
func sourceHash(source string) string {
	hashBytes := sha3.Sum512([]byte(source))
	hashHex := hex.EncodeToString(hashBytes[:])
	return hashHex
}
