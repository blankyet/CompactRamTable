package compact_table

import (
	"bytes"
	"slices"
)

type InitialTypeEnum = uint8

const (
	TypeString InitialTypeEnum = iota

	TypeBool
	TypeChar

	TypeInt32
	TypeInt64
	TypeUInt32
	TypeUInt64

	TypeFloat
	TypeDouble

	TypeIndex
	TypeExternalIndex
)

type FinalTypeEnum = uint8

const (
	TypeTokenizedString FinalTypeEnum = iota
	TypeBit
	TypeByte

	Type2Byte
	Type4Byte
	Type8Byte
)

type CompactingStrategyEnum = uint8

const (
	CompactingRaw CompactingStrategyEnum = iota
	CompactingScaled
	CompactingEnumed

	CompactingIndexed

	CompactingTokenized
)

type TokenizerTypeEnum = uint8

const (
	TokenizerNone TokenizerTypeEnum = iota
	TokenizerStandardText
	TokenizerAliases
	TokenizerUrls
)

type FieldData struct {
	// Required
	Name        string
	InitialType InitialTypeEnum
	// Optional
	Tokenizer         TokenizerTypeEnum
	ExternalTableName string
	EnumNames         []string
	NullValues        [][]byte
	IsList            bool
	WithSearchIndex   bool
}

func (fd *FieldData) IsNullBlob(blob []byte) bool {
	return slices.ContainsFunc(fd.NullValues, func(n []byte) bool {
		return bytes.Equal(n, blob)
	})
}
