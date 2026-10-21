package npk

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"golang.org/x/text/encoding/korean"
)

const (
	npkHeaderSize = 20
	npkEntrySize  = 264
)

// CopySubset writes a new NPK containing exactly the given members, copying
// each member's bytes verbatim from src. Only offsets change.
//
// Names are written from Entry.RawName, the encrypted 256-byte field filled by
// Open. They are deliberately not re-encoded from Entry.Name: that would need
// EUC-KR plus XOR, and decodeNPKName trims surrounding spaces, so names with
// leading or trailing spaces could not round-trip. Callers that build entries
// by hand must fill RawName; Name alone is not written.
//
// The layout is the 20-byte header, the 264-byte-per-entry table, then member
// data aligned to 4-byte offsets. An empty entries slice produces a valid
// zero-member container. Any error stops the copy immediately; the caller is
// expected to write into a temporary file and discard it on failure.
func CopySubset(dst io.Writer, src io.ReaderAt, entries []Entry) error {
	if len(entries) > maxNPKEntries {
		return fmt.Errorf("npk: 成员数量 %d 超过上限 %d", len(entries), maxNPKEntries)
	}

	headerSize := int64(npkHeaderSize) + int64(len(entries))*npkEntrySize
	table := make([]byte, len(entries)*npkEntrySize)
	offsets := make([]int64, len(entries))
	position := headerSize
	for index, entry := range entries {
		position = alignNPKOffset(position)
		if entry.Offset < 0 || entry.Size < 0 {
			return fmt.Errorf("npk: 第 %d 个成员（%s）的源区间非法: offset=%d size=%d", index, entry.Name, entry.Offset, entry.Size)
		}
		end := position + entry.Size
		// Written offsets are stored as int32, so the whole member range must fit.
		if end > math.MaxInt32 {
			return fmt.Errorf("npk: 第 %d 个成员（%s）的写入偏移超过 int32: offset=%d size=%d", index, entry.Name, position, entry.Size)
		}
		offsets[index] = position
		base := index * npkEntrySize
		binary.LittleEndian.PutUint32(table[base:], uint32(position))
		binary.LittleEndian.PutUint32(table[base+4:], uint32(entry.Size))
		copy(table[base+8:base+npkEntrySize], entry.RawName[:])
		position = end
	}

	header := make([]byte, npkHeaderSize)
	copy(header, npkMagic)
	binary.LittleEndian.PutUint32(header[16:], uint32(len(entries)))
	if err := writeNPKAll(dst, header); err != nil {
		return err
	}
	if err := writeNPKAll(dst, table); err != nil {
		return err
	}

	written := headerSize
	buffer := make([]byte, 128*1024)
	var padding [3]byte
	for index, entry := range entries {
		if gap := offsets[index] - written; gap > 0 {
			if err := writeNPKAll(dst, padding[:gap]); err != nil {
				return err
			}
			written += gap
		}
		copied, err := io.CopyBuffer(dst, io.NewSectionReader(src, entry.Offset, entry.Size), buffer)
		written += copied
		if err != nil {
			return fmt.Errorf("npk: 复制第 %d 个成员（%s）失败: %w", index, entry.Name, err)
		}
		if copied != entry.Size {
			return fmt.Errorf("npk: 复制第 %d 个成员（%s）时源数据不足: %w", index, entry.Name, io.ErrUnexpectedEOF)
		}
	}
	return nil
}

func alignNPKOffset(offset int64) int64 {
	return (offset + 3) &^ 3
}

// EncodeName builds the encrypted 256-byte name field from a plain member name.
//
// CopySubset deliberately does not use it: re-encoding is lossy for names with
// leading or trailing spaces because decodeNPKName trims them. It exists for
// callers that have to invent a member (tests, future container authoring).
func EncodeName(name string) ([256]byte, error) {
	var result [256]byte
	encoded, err := korean.EUCKR.NewEncoder().Bytes([]byte(name))
	if err != nil {
		return result, fmt.Errorf("npk: 名字无法用 EUC-KR 编码: %w", err)
	}
	if len(encoded) > len(result)-1 {
		return result, fmt.Errorf("npk: 名字超过 %d 字节", len(result)-1)
	}
	for index, value := range encoded {
		result[index] = value ^ npkFilenameKey[index]
	}
	// 名字之后必须是"明文的 0"，也就是密文等于 key 本身；留 0 会解码成 key 字节，
	// 名字后面就多出一串垃圾，连终止符都找不到。
	for index := len(encoded); index < len(result); index++ {
		result[index] = npkFilenameKey[index]
	}
	return result, nil
}

func writeNPKAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
