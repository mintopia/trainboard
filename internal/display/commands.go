package display

// SSD1322 command opcodes (datasheet §9). Values referenced by the init
// sequence and flush path; pinned by golden-byte tests.
//
//nolint:unused // consumed by the init/flush sequences implemented in Task 3/6/7.
const (
	cmdEnableGrayTable  = 0x00
	cmdSetColumnAddr    = 0x15
	cmdWriteRAM         = 0x5C
	cmdSetRowAddr       = 0x75
	cmdSetRemap         = 0xA0
	cmdSetStartLine     = 0xA1
	cmdSetDisplayOffset = 0xA2
	cmdSetDisplayNormal = 0xA6
	cmdExitPartial      = 0xA9
	cmdFunctionSelect   = 0xAB
	cmdDisplayOff       = 0xAE
	cmdDisplayOn        = 0xAF
	cmdSetPhaseLength   = 0xB1
	cmdSetClockDivider  = 0xB3
	cmdDisplayEnhanceA  = 0xB4
	cmdSetGPIO          = 0xB5
	cmdSecondPrecharge  = 0xB6
	cmdSetContrast      = 0xC1
	cmdMasterContrast   = 0xC7
	cmdSetMuxRatio      = 0xCA
	cmdDisplayEnhanceB  = 0xD1
	cmdSetCommandLock   = 0xFD
	cmdSetPrechargeVolt = 0xBB
	cmdSetVCOMH         = 0xBE

	// Column window for a 256px panel in the 480px RAM.
	colStart = 0x1C
	colEnd   = 0x5B
	rowStart = 0x00
	rowEnd   = 0x3F
)
