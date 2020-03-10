package packet

import (
	"errors"
	"io"

	"github.com/32bitkid/bitreader"
)

/*
https://github.com/videolan/vlc/blob/master/modules/demux/mpeg
*/
type DecPSPackage struct {
	systemClockReferenceBase      uint64
	systemClockReferenceExtension uint64
	programMuxRate                uint32

	RawData         []byte
	RawLen          int
	videoStreamType uint32
	audioStreamType uint32

	Iframe bool
	Pts    uint64
	Dts    uint64
}

func (dec *DecPSPackage) DecPackHeader(br bitreader.BitReader) error {

	startcode, err := br.Read32(32)
	if err != nil {
		return err
	}
	if startcode == StartCodeAudio {
		audioPayloadLen, err := br.Read32(16)
		if err != nil {
			return err
		}
		br.Skip(uint(audioPayloadLen * 8))
		startcode, err = br.Read32(32)
		if err != nil {
			return err
		}
	}
	if startcode != StartCodePS {
		return ErrNotFoundStartCode
	}

	if marker, err := br.Read32(2); err != nil {
		return err
	} else if marker != 0x01 {
		return ErrMarkerBit
	}

	if s, err := br.Read32(3); err != nil {
		return err
	} else {
		dec.systemClockReferenceBase |= uint64(s << 30)
	}
	if marker, err := br.Read32(1); err != nil {
		return err
	} else if marker != 0x01 {
		return ErrMarkerBit
	}

	if s, err := br.Read32(15); err != nil {
		return err
	} else {
		dec.systemClockReferenceBase |= uint64(s << 15)
	}
	if marker, err := br.Read32(1); err != nil {
		return err
	} else if marker != 0x01 {
		return ErrMarkerBit
	}
	if s, err := br.Read32(15); err != nil {
		return err
	} else {
		dec.systemClockReferenceBase |= uint64(s)
	}
	if marker, err := br.Read32(1); err != nil {
		return err
	} else if marker != 0x01 {
		return ErrMarkerBit
	}
	if s, err := br.Read32(9); err != nil {
		return err
	} else {
		dec.systemClockReferenceExtension |= uint64(s)
	}
	if marker, err := br.Read32(1); err != nil {
		return err
	} else if marker != 0x01 {
		return ErrMarkerBit
	}

	if pmr, err := br.Read32(22); err != nil {
		return err
	} else {
		dec.programMuxRate |= pmr
	}
	if marker, err := br.Read32(1); err != nil {
		return err
	} else if marker != 0x01 {
		return ErrMarkerBit
	}
	if marker, err := br.Read32(1); err != nil {
		return err
	} else if marker != 0x01 {
		return ErrMarkerBit
	}

	if err := br.Skip(5); err != nil {
		return err
	}
	if psl, err := br.Read32(3); err != nil {
		return err
	} else {
		br.Skip(uint(psl * 8))
	}

	// 判断是否位关键帧， I帧会有system头 systemap头
	for {
		nextStartCode, err := br.Read32(32)
		if err != nil {
			return err
		}

		switch nextStartCode {
		case StartCodeSYS:
			if err := dec.decSystemHeader(br); err != nil {
				return err
			}
		case StartCodeMAP:
			if err := dec.decProgramStreamMap(br); err != nil {
				return err
			}
			dec.Iframe = true
		case MEPGProgramEndCode:
		//	return dec.RawData[:dec.RawLen], nil
		default:
			VideoCode := nextStartCode & 0xfffffff0
			if VideoCode == StartCodeVideo {
				if err := dec.decPESPacket(br); err != nil {
					return err
				}
			}
		}
	}
}

func (dec *DecPSPackage) decSystemHeader(br bitreader.BitReader) error {
	syslens, err := br.Read32(16)
	if err != nil {
		return err
	}
	// drop rate video audio bound and lock flag
	syslens -= 6
	br.Skip(6 * 8)

	// ONE WAY: do not to parse the stream  and skip the buffer
	//br.Skip(syslen * 8)

	// TWO WAY: parse every stream info
	for syslens > 0 {
		if nextbits, err := br.Peek32(1); err != nil {
			return err
		} else if nextbits == 1 {
			break
		}

		if _, err := br.Read32(8); err != nil {
			return err
		}
		if _, err := br.Read32(2); err != nil {
			return err
		}
		if _, err := br.Read1(); err != nil {
			return err
		}
		if _, err := br.Read32(13); err != nil {
			return err
		}
	}
	return nil
}

func (dec *DecPSPackage) decProgramStreamMap(br bitreader.BitReader) error {
	psm, err := br.Read32(16)
	if err != nil {
		return err
	}
	//drop psm version infor
	br.Skip(16)
	psm -= 2
	if programStreamInfoLen, err := br.Read32(16); err != nil {
		return err
	} else {
		br.Skip(uint(programStreamInfoLen * 8))
		psm -= (programStreamInfoLen + 2)
	}
	programStreamMapLen, err := br.Read32(16)
	if err != nil {
		return err
	}
	psm -= (2 + programStreamMapLen)
	for programStreamMapLen > 0 {
		streamType, err := br.Read32(8)
		if err != nil {
			return err
		}
		if elementaryStreamID, err := br.Read32(8); err != nil {
			return err
		} else if elementaryStreamID >= 0xe0 && elementaryStreamID <= 0xef {
			dec.videoStreamType = streamType
		} else if elementaryStreamID >= 0xc0 && elementaryStreamID <= 0xdf {
			dec.audioStreamType = streamType
		}
		if elementaryStreamInfoLength, err := br.Read32(16); err != nil {
			return err
		} else {
			br.Skip(uint(elementaryStreamInfoLength * 8))
			programStreamMapLen -= (4 + elementaryStreamInfoLength)
		}

	}

	// crc 32
	if psm != 4 {
		return ErrFormatPack
	}
	br.Skip(32)
	return nil
}

var ErrMarkerNotFound = errors.New("marker not found")

func readTimeStamp(marker uint64, reader bitreader.BitReader) (uint64, uint32, error) {

	var (
		timeStamp uint64
		err       error
		val       uint64
	)

	val, err = reader.Read64(4)
	if val != marker || err != nil {
		return 0, 0, ErrMarkerNotFound
	}

	val, err = reader.Read64(3)
	if err != nil {
		return 0, 0, err
	}

	timeStamp = timeStamp | (val << 30)

	val, err = reader.Read64(1)
	if val != 1 || err != nil {
		return 0, 0, ErrMarkerNotFound
	}

	val, err = reader.Read64(15)
	if err != nil {
		return 0, 0, err
	}

	timeStamp = timeStamp | (val << 15)

	val, err = reader.Read64(1)
	if val != 1 || err != nil {
		return 0, 0, ErrMarkerNotFound
	}

	val, err = reader.Read64(15)
	if err != nil {
		return 0, 0, err
	}

	timeStamp = timeStamp | val

	val, err = reader.Read64(1)
	if val != 1 || err != nil {
		return 0, 0, ErrMarkerNotFound
	}

	return timeStamp, 5, nil
}

func (dec *DecPSPackage) decPESPacket(br bitreader.BitReader) error {

	payloadlen, err := br.Read32(16)
	if err != nil {
		return err
	}
	br.Skip(8)
	ptsFlag, err := br.Read32(2)
	if err != nil {
		return err
	}
	br.Skip(6)

	payloadlen -= 2
	if pesHeaderDataLen, err := br.Read32(8); err != nil {
		return err
	} else {
		payloadlen--

		var len uint32 = 0
		if ptsFlag == 2 && pesHeaderDataLen >= 5 {
			dec.Pts, len, err = readTimeStamp(2, br)
			if err != nil {
				return err
			}
			br.Skip(uint((pesHeaderDataLen - len) * 8))
		} else if ptsFlag == 3 && pesHeaderDataLen >= 10 {
			dec.Pts, len, err = readTimeStamp(3, br)
			if err != nil {
				return err
			}
			dec.Dts, len, err = readTimeStamp(1, br)
			if err != nil {
				return err
			}
			br.Skip(uint((pesHeaderDataLen - len - len) * 8))

		} else {
			br.Skip(uint(pesHeaderDataLen * 8))
		}
		payloadlen -= pesHeaderDataLen
	}

	payloaddata := make([]byte, payloadlen)
	if _, err := io.ReadAtLeast(br, payloaddata, int(payloadlen)); err != nil {
		return err
	} else {
		copy(dec.RawData[dec.RawLen:], payloaddata)
		dec.RawLen += int(payloadlen)
	}

	return nil
}
