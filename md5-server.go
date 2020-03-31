//+build !noasm,!appengine

/*
 * Minio Cloud Storage, (C) 2020 Minio, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package md5simd

import (
	"encoding/binary"
	"errors"
	"hash"
	"sort"
	"sync/atomic"
	"time"
	"unsafe"
	"fmt"
	"bytes"
)

const BlockSize = 64
const Size = 16
const chunk = BlockSize

//go:noescape
//func sha256X16Avx512(digests *[512]byte, scratch *[512]byte, table *[512]uint64, mask []uint64, inputs [16][]byte)

// Md5ServerUID - Do not start at 0 but next multiple of 8 so as to be able to
// differentiate with default initialiation value of 0
const Md5ServerUID = 8
var uidCounter uint64

// NewMd5 - initialize parallel Md5 implementation.
func NewMd5(md5srv *Md5Server) hash.Hash {
	uid := atomic.AddUint64(&uidCounter, 1)
	return &Md5Digest{uid: uid, md5srv: md5srv}
}

// Md5Digest - Type for computing MD5 using AVX2
type Md5Digest struct {
	uid     uint64
	md5srv  *Md5Server
	x       [chunk]byte
	nx      int
	len     uint64
	final   bool
	result  [Size]byte
}

// Size - Return size of checksum
func (d *Md5Digest) Size() int { return Size }

// BlockSize - Return blocksize of checksum
func (d Md5Digest) BlockSize() int { return BlockSize }

// Reset - reset sha digest to its initial values
func (d *Md5Digest) Reset() {
	d.md5srv.blocksCh <- blockInput{uid: d.uid, reset: true}
	d.nx = 0
	d.len = 0
	d.final = false
}

// Write to digest
func (d *Md5Digest) Write(p []byte) (nn int, err error) {

	if d.final {
		return 0, errors.New("Md5Digest already finalized. Reset first before writing again")
	}

	nn = len(p)
	d.len += uint64(nn)
	if d.nx > 0 {
		n := copy(d.x[d.nx:], p)
		d.nx += n
		if d.nx == chunk {
			d.md5srv.blocksCh <- blockInput{uid: d.uid, msg: d.x[:]}
			d.nx = 0
		}
		p = p[n:]
	}
	if len(p) >= chunk {
		n := len(p) &^ (chunk - 1)
		d.md5srv.blocksCh <- blockInput{uid: d.uid, msg: p[:n]}
		p = p[n:]
	}
	if len(p) > 0 {
		d.nx = copy(d.x[:], p)
	}
	return
}

// Sum - Return sha256 sum in bytes
func (d *Md5Digest) Sum(in []byte) (result []byte) {

	if d.final {
		return append(in, d.result[:]...)
	}

	trail := make([]byte, 0, 128)
	trail = append(trail, d.x[:d.nx]...)

	len := d.len
	// Padding.  Add a 1 bit and 0 bits until 56 bytes mod 64.
	var tmp [64]byte
	tmp[0] = 0x80
	if len%64 < 56 {
		trail = append(trail, tmp[0:56-len%64]...)
	} else {
		trail = append(trail, tmp[0:64+56-len%64]...)
	}
	d.nx = 0

	// Length in bits.
	len <<= 3
	for i := uint(0); i < 8; i++ {
		tmp[i] = byte(len >> (56 - 8*i))
	}
	trail = append(trail, tmp[0:8]...)

	sumCh := make(chan [Size]byte)
	d.md5srv.blocksCh <- blockInput{uid: d.uid, msg: trail, final: true, sumCh: sumCh}
	d.result = <-sumCh
	d.final = true
	return append(in, d.result[:]...)
}

// Interface function to assembly code
func blockMd5(digests *[128]byte, input [8][]byte/*, mask []uint64*/) [8][Size]byte {

	var s digest8

	s.v0[0] = 0x67452301
	s.v1[0] = 0xefcdab89
	s.v2[0] = 0x98badcfe
	s.v3[0] = 0x10325476

	for i := 1; i < 8; i++ {
		s.v0[i] = s.v0[i-1]
		s.v1[i] = s.v1[i-1]
		s.v2[i] = s.v2[i-1]
		s.v3[i] = s.v3[i-1]
	}

	var bufs [8]int32

	base := bytes.Repeat([]byte("h"), 1024)

	var cache cache8 // stack storage for block8 tmp state

	block8(&s.v0[0], uintptr(unsafe.Pointer(&(base[0]))), &bufs[0], &cache[0], 64 /*n*/)

	//if s.v0[0] != 0x89d4ff56 || s.v1[0] != 0x125cd962 || s.v2[0] != 0x69cade33 || s.v3[0] != 0x0033e325 { // aaaaa
	//	panic("Error in lane 1")
	//}

	//if s.v0[0] != 0xf230419a || s.v1[0] != 0x5009fe4e || s.v2[0] != 0xbac6852f || s.v3[0] != 0xe631cc2f { // bbbbb
	//	panic("Error in lane 1")
	//}

	//if s.v0[0] != 0xb6679971 || s.v1[0] != 0x5767705a || s.v2[0] != 0x16296ec1 || s.v3[0] != 0x2556a2f2 { // ccccc
	//	panic("Error in lane 1")
	//}

	//if s.v0[0] != 0xd2d0e59c || s.v1[0] != 0x6f1aa3d8 || s.v2[0] != 0x8b8bdf88 || s.v3[0] != 0x9f4c6912 { // ddddd
	//	panic("Error in lane 1")
	//}

	//if s.v0[0] != 0x441e8ef1 || s.v1[0] != 0xafeb8e56 || s.v2[0] != 0x6d00d8ae || s.v3[0] != 0x91921784 { // eeeee
	//	panic("Error in lane 1")
	//}

	//if s.v0[0] != 0x4a3fdc71 || s.v1[0] != 0x6645d0bb || s.v2[0] != 0x84d4e6a6 || s.v3[0] != 0xd6ea6f44 { // fffff
	//	panic("Error in lane 1")
	//}

	//if s.v0[0] != 0x698884d3 || s.v1[0] != 0xdc5ba7ad || s.v2[0] != 0xebf7c759 || s.v3[0] != 0x4208b0db { // ggggg
	//	panic("Error in lane 1")
	//}

	if s.v0[0] != 0x53a1b03e || s.v1[0] != 0x66d53e23 || s.v2[0] != 0xafdc6126 || s.v3[0] != 0xb39c4fdd { // hhhhh
		panic("Error in lane 1")
	}

	fmt.Printf("%08x-%08x-%08x-%08x-%08x-%08x-%08x-%08x\n", s.v0[0], s.v0[1], s.v0[2], s.v0[3], s.v0[4], s.v0[5], s.v0[6], s.v0[7])
	fmt.Printf("%08x-%08x-%08x-%08x-%08x-%08x-%08x-%08x\n", s.v1[0], s.v1[1], s.v1[2], s.v1[3], s.v1[4], s.v1[5], s.v1[6], s.v1[7])
	fmt.Printf("%08x-%08x-%08x-%08x-%08x-%08x-%08x-%08x\n", s.v2[0], s.v2[1], s.v2[2], s.v2[3], s.v2[4], s.v2[5], s.v2[6], s.v2[7])
	fmt.Printf("%08x-%08x-%08x-%08x-%08x-%08x-%08x-%08x\n", s.v3[0], s.v3[1], s.v3[2], s.v3[3], s.v3[4], s.v3[5], s.v3[6], s.v3[7])

	output := [8][Size]byte{}
	for i := 0; i < 8; i++ {
		output[i] = getDigest(i, digests[:])
	}

	return output
}

func getDigest(index int, state []byte) (sum [Size]byte) {
	//for j := 0; j < 8; j += 2 {
	//	for i := index*4 + j*Size; i < index*4+(j+1)*Size; i += Size {
	//		binary.BigEndian.PutUint32(sum[j*2:], binary.LittleEndian.Uint32(state[i:i+4]))
	//	}
	//}
	return
}

// Message to send across input channel
type blockInput struct {
	uid   uint64
	msg   []byte
	reset bool
	final bool
	sumCh chan [Size]byte
}

// Md5Server - Type to implement parallel handling of MD5 invocations
type Md5Server struct {
	blocksCh chan blockInput       // Input channel
	totalIn  int                   // Total number of inputs waiting to be processed
	lanes    [8]Md5LaneInfo    // Array with info per lane (out of 8)
	digests  map[uint64][Size]byte // Map of uids to (interim) digest results
}

// Md5LaneInfo - Info for each lane
type Md5LaneInfo struct {
	uid      uint64          // unique identification for this MD5 processing
	block    []byte          // input block to be processed
	outputCh chan [Size]byte // channel for output result
}

// NewMd5Server - Create new object for parallel processing handling
func NewMd5Server() *Md5Server {
	md5srv := &Md5Server{}
	md5srv.digests = make(map[uint64][Size]byte)
	md5srv.blocksCh = make(chan blockInput)

	// Start a single thread for reading from the input channel
	go md5srv.Process()
	return md5srv
}

// Process - Sole handler for reading from the input channel
func (md5srv *Md5Server) Process() {
	for {
		select {
		case block := <-md5srv.blocksCh:
			if block.reset {
				md5srv.reset(block.uid)
				continue
			}
			index := block.uid & 0xf
			// fmt.Println("Adding message:", block.uid, index)

			if md5srv.lanes[index].block != nil { // If slot is already filled, process all inputs
				//fmt.Println("Invoking Blocks()")
				md5srv.blocks()
			}
			md5srv.totalIn++
			md5srv.lanes[index] = Md5LaneInfo{uid: block.uid, block: block.msg}
			if block.final {
				md5srv.lanes[index].outputCh = block.sumCh
			}
			if md5srv.totalIn == len(md5srv.lanes) {
				// fmt.Println("Invoking Blocks() while FULL: ")
				md5srv.blocks()
			}

			// TODO: test with larger timeout
		case <-time.After(1 * time.Microsecond):
			for _, lane := range md5srv.lanes {
				if lane.block != nil { // check if there is any input to process
					// fmt.Println("Invoking Blocks() on TIMEOUT: ")
					md5srv.blocks()
					break // we are done
				}
			}
		}
	}
}

// Do a reset for this calculation
func (md5srv *Md5Server) reset(uid uint64) {

	// Check if there is a message still waiting to be processed (and remove if so)
	for i, lane := range md5srv.lanes {
		if lane.uid == uid {
			if lane.block != nil {
				md5srv.lanes[i] = Md5LaneInfo{} // clear message
				md5srv.totalIn--
			}
		}
	}

	// Delete entry from hash map
	delete(md5srv.digests, uid)
}

// Invoke assembly and send results back
func (md5srv *Md5Server) blocks() {

	inputs := [8][]byte{}
	for i := range inputs {
		inputs[i] = md5srv.lanes[i].block
	}

//	mask := expandMask(genMask(md5srv))
	outputs := blockMd5(md5srv.getDigests(), inputs) // , mask)

	md5srv.totalIn = 0
	for i := 0; i < len(outputs); i++ {
		uid, outputCh := md5srv.lanes[i].uid, md5srv.lanes[i].outputCh
		md5srv.digests[uid] = outputs[i]
		md5srv.lanes[i] = Md5LaneInfo{}

		if outputCh != nil {
			// Send back result
			outputCh <- outputs[i]
			delete(md5srv.digests, uid) // Delete entry from hashmap
		}
	}
}

func (md5srv *Md5Server) Write(uid uint64, p []byte) (nn int, err error) {
	md5srv.blocksCh <- blockInput{uid: uid, msg: p}
	return len(p), nil
}

// Sum - return sha256 sum in bytes for a given sum id.
func (md5srv *Md5Server) Sum(uid uint64, p []byte) [16]byte {
	sumCh := make(chan [16]byte)
	md5srv.blocksCh <- blockInput{uid: uid, msg: p, final: true, sumCh: sumCh}
	return <-sumCh
}

func (md5srv *Md5Server) getDigests() *[128]byte {
	digests := [128]byte{}
	for i, lane := range md5srv.lanes {
		a, ok := md5srv.digests[lane.uid]
		if ok {
			binary.BigEndian.PutUint32(digests[(i+0*8)*4:], binary.LittleEndian.Uint32(a[0:4]))
			binary.BigEndian.PutUint32(digests[(i+1*8)*4:], binary.LittleEndian.Uint32(a[4:8]))
			binary.BigEndian.PutUint32(digests[(i+2*8)*4:], binary.LittleEndian.Uint32(a[8:12]))
			binary.BigEndian.PutUint32(digests[(i+3*8)*4:], binary.LittleEndian.Uint32(a[12:16]))
		} else {
			binary.LittleEndian.PutUint32(digests[(i+0*8)*4:], 0) // init0)
			binary.LittleEndian.PutUint32(digests[(i+1*8)*4:], 0) // init1)
			binary.LittleEndian.PutUint32(digests[(i+2*8)*4:], 0) // init2)
			binary.LittleEndian.PutUint32(digests[(i+3*8)*4:], 0) // init3)
		}
	}
	return &digests
}

// Helper struct for sorting blocks based on length
type lane struct {
	len uint
	pos uint
}

type lanes []lane

func (lns lanes) Len() int           { return len(lns) }
func (lns lanes) Swap(i, j int)      { lns[i], lns[j] = lns[j], lns[i] }
func (lns lanes) Less(i, j int) bool { return lns[i].len < lns[j].len }

// Helper struct for
type maskRounds struct {
	mask   uint64
	rounds uint64
}

func genMask(input [8][]byte) [8]maskRounds {

	// Sort on blocks length small to large
	var sorted [8]lane
	for c, inpt := range input {
		sorted[c] = lane{uint(len(inpt)), uint(c)}
	}
	sort.Sort(lanes(sorted[:]))

	// Create mask array including 'rounds' between masks
	m, round, index := uint64(0xffff), uint64(0), 0
	var mr [8]maskRounds
	for _, s := range sorted {
		if s.len > 0 {
			if uint64(s.len)>>6 > round {
				mr[index] = maskRounds{m, (uint64(s.len) >> 6) - round}
				index++
			}
			round = uint64(s.len) >> 6
		}
		m = m & ^(1 << uint(s.pos))
	}

	return mr
}

// TODO: remove function
func expandMask(mr [8]maskRounds) []uint64 {
	size := uint64(0)
	for _, r := range mr {
		size += r.rounds
	}
	result, index := make([]uint64, size), 0
	for _, r := range mr {
		for j := uint64(0); j < r.rounds; j++ {
			result[index] = r.mask
			index++
		}
	}
	return result
}
