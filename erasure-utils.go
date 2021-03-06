/*
 * Minio Cloud Storage, (C) 2016 Minio, Inc.
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

package main

import (
	"bytes"
	"errors"
	"hash"
	"io"

	"github.com/klauspost/reedsolomon"
	"github.com/minio/blake2b-simd"
)

// newHashWriters - inititialize a slice of hashes for the disk count.
func newHashWriters(diskCount int, algo string) []hash.Hash {
	hashWriters := make([]hash.Hash, diskCount)
	for index := range hashWriters {
		hashWriters[index] = newHash(algo)
	}
	return hashWriters
}

// newHash - gives you a newly allocated hash depending on the input algorithm.
func newHash(algo string) hash.Hash {
	switch algo {
	case "blake2b":
		return blake2b.New512()
	// Add new hashes here.
	default:
		// Default to blake2b.
		return blake2b.New512()
	}
}

// hashSum calculates the hash of the entire path and returns.
func hashSum(disk StorageAPI, volume, path string, writer hash.Hash) ([]byte, error) {
	// Allocate staging buffer of 128KiB for copyBuffer.
	buf := make([]byte, readSizeV1)

	// Copy entire buffer to writer.
	if err := copyBuffer(writer, disk, volume, path, buf); err != nil {
		return nil, err
	}

	// Return the final hash sum.
	return writer.Sum(nil), nil
}

// getDataBlockLen - get length of data blocks from encoded blocks.
func getDataBlockLen(enBlocks [][]byte, dataBlocks int) int {
	size := 0
	// Figure out the data block length.
	for _, block := range enBlocks[:dataBlocks] {
		size += len(block)
	}
	return size
}

// Writes all the data blocks from encoded blocks until requested
// outSize length. Provides a way to skip bytes until the offset.
func writeDataBlocks(dst io.Writer, enBlocks [][]byte, dataBlocks int, offset int64, length int64) (int64, error) {
	// Offset and out size cannot be negative.
	if offset < 0 || length < 0 {
		return 0, errUnexpected
	}

	// Do we have enough blocks?
	if len(enBlocks) < dataBlocks {
		return 0, reedsolomon.ErrTooFewShards
	}

	// Do we have enough data?
	if int64(getDataBlockLen(enBlocks, dataBlocks)) < length {
		return 0, reedsolomon.ErrShortData
	}

	// Counter to decrement total left to write.
	write := length

	// Counter to increment total written.
	totalWritten := int64(0)

	// Write all data blocks to dst.
	for _, block := range enBlocks[:dataBlocks] {
		// Skip blocks until we have reached our offset.
		if offset >= int64(len(block)) {
			// Decrement offset.
			offset -= int64(len(block))
			continue
		} else {
			// Skip until offset.
			block = block[offset:]

			// Reset the offset for next iteration to read everything
			// from subsequent blocks.
			offset = 0
		}
		// We have written all the blocks, write the last remaining block.
		if write < int64(len(block)) {
			n, err := io.Copy(dst, bytes.NewReader(block[:write]))
			if err != nil {
				return 0, err
			}
			totalWritten += n
			break
		}
		// Copy the block.
		n, err := io.Copy(dst, bytes.NewReader(block))
		if err != nil {
			return 0, err
		}

		// Decrement output size.
		write -= n

		// Increment written.
		totalWritten += n
	}

	// Success.
	return totalWritten, nil
}

// chunkSize is roughly BlockSize/DataBlocks.
// chunkSize is calculated such that chunkSize*DataBlocks accommodates BlockSize bytes.
// So chunkSize*DataBlocks can be slightly larger than BlockSize if BlockSize is not divisible by
// DataBlocks. The extra space will have 0-padding.
func getChunkSize(blockSize int64, dataBlocks int) int64 {
	return (blockSize + int64(dataBlocks) - 1) / int64(dataBlocks)
}

// copyBuffer - copies from disk, volume, path to input writer until either EOF
// is reached at volume, path or an error occurs. A success copyBuffer returns
// err == nil, not err == EOF. Because copyBuffer is defined to read from path
// until EOF. It does not treat an EOF from ReadFile an error to be reported.
// Additionally copyBuffer stages through the provided buffer; otherwise if it
// has zero length, returns error.
func copyBuffer(writer io.Writer, disk StorageAPI, volume string, path string, buf []byte) error {
	// Error condition of zero length buffer.
	if buf != nil && len(buf) == 0 {
		return errors.New("empty buffer in readBuffer")
	}

	// Starting offset for Reading the file.
	startOffset := int64(0)

	// Read until io.EOF.
	for {
		n, err := disk.ReadFile(volume, path, startOffset, buf)
		if n > 0 {
			m, wErr := writer.Write(buf[:n])
			if wErr != nil {
				return wErr
			}
			if int64(m) != n {
				return io.ErrShortWrite
			}
		}
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return err
		}
		// Progress the offset.
		startOffset += n
	}

	// Success.
	return nil
}
