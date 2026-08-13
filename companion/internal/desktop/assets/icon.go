package assets

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
)

// IconPNG returns a compact monochrome 22x22 Deck outline. Keeping the icon
// generated from source avoids a separate runtime asset while giving macOS a
// template image and Windows a normal notification-area image.
func IconPNG() []byte {
	const width = 22
	const height = 22
	pixels := make([]byte, 0, height*(1+width*4))
	for y := 0; y < height; y++ {
		pixels = append(pixels, 0)
		for x := 0; x < width; x++ {
			border := (x >= 2 && x <= 19 && (y == 4 || y == 17)) ||
				(y >= 4 && y <= 17 && (x == 2 || x == 19))
			stand := (y == 18 && x >= 9 && x <= 12) ||
				(y == 19 && x >= 7 && x <= 14)
			alpha := byte(0)
			if border || stand {
				alpha = 255
			}
			pixels = append(pixels, 0, 0, 0, alpha)
		}
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(pixels)
	_ = writer.Close()
	var png bytes.Buffer
	png.Write([]byte("\x89PNG\r\n\x1a\n"))
	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header[0:4], width)
	binary.BigEndian.PutUint32(header[4:8], height)
	header[8], header[9] = 8, 6
	writeChunk(&png, "IHDR", header)
	writeChunk(&png, "IDAT", compressed.Bytes())
	writeChunk(&png, "IEND", nil)
	return png.Bytes()
}

func writeChunk(target *bytes.Buffer, kind string, data []byte) {
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(data)))
	target.Write(length)
	target.WriteString(kind)
	target.Write(data)
	checksum := crc32.NewIEEE()
	_, _ = checksum.Write([]byte(kind))
	_, _ = checksum.Write(data)
	binary.BigEndian.PutUint32(length, checksum.Sum32())
	target.Write(length)
}
