package signal

import (
	"encoding/binary"
	"log"
	"os"
	"time"
	"fmt"
)

// DumpRecord 144 bytes
type DumpRecord struct {
	TSF        uint64
	TargetFreq float32
	EmitterID  uint8
	Padding    [3]uint8
	Samples    [32]float32
}

type Dumper struct {
	ch       chan DumpRecord
	file     *os.File
	stop     chan struct{}
	isActive bool
}

var globalDumper *Dumper

func init() {
	globalDumper = &Dumper{
		ch:   make(chan DumpRecord, 8192),
		stop: make(chan struct{}),
	}
	go globalDumper.run()
}

func (d *Dumper) run() {
	for {
		select {
		case <-d.stop:
			if d.file != nil {
				d.file.Close()
				d.file = nil
			}
			return
		case r := <-d.ch:
			if d.isActive && d.file != nil {
				err := binary.Write(d.file, binary.LittleEndian, &r)
				if err != nil {
					log.Printf("[DUMPER] write error: %v", err)
				}
			}
		}
	}
}

func ToggleDump(sessionName string) bool {
	if globalDumper == nil {
		return false
	}
	if !globalDumper.isActive {
		prefix := "dump"
		if sessionName != "" {
			prefix = sessionName
		}
		name := fmt.Sprintf("%s_%s.bin", prefix, time.Now().Format("20060102_150405"))
		f, err := os.Create(name)
		if err != nil {
			log.Printf("[DUMPER] failed to create file: %v", err)
			return false
		}
		globalDumper.file = f
		globalDumper.isActive = true
		log.Printf("[DUMPER] started recording to %s", name)
		return true
	} else {
		globalDumper.isActive = false
		if globalDumper.file != nil {
			globalDumper.file.Close()
			globalDumper.file = nil
		}
		log.Printf("[DUMPER] stopped recording")
		return false
	}
}

func IsDumping() bool {
	if globalDumper == nil {
		return false
	}
	return globalDumper.isActive
}

func PushDumpRecord(r DumpRecord) {
	if globalDumper != nil && globalDumper.isActive {
		select {
		case globalDumper.ch <- r:
		default:
			// log.Printf("[DUMPER] overflow, dropping record")
		}
	}
}
