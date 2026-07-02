package display

// SSD1322 is a 256x64 4-bit greyscale OLED controller driven over a Transport.
type SSD1322 struct {
	t Transport
}

// New wraps a Transport in an SSD1322 driver.
func New(t Transport) *SSD1322 { return &SSD1322{t: t} }

// Init resets the panel and issues the power-on configuration sequence,
// leaving the display on with normal (non-inverted) greyscale output.
func (d *SSD1322) Init() error {
	if err := d.t.Reset(); err != nil {
		return err
	}
	seq := []struct {
		cmd  byte
		args []byte
	}{
		{cmdSetCommandLock, []byte{0x12}},   // unlock commands
		{cmdDisplayOff, nil},                // sleep during config
		{cmdSetClockDivider, []byte{0x91}},  // osc freq / divider
		{cmdSetMuxRatio, []byte{0x3F}},      // 64 MUX
		{cmdSetDisplayOffset, []byte{0x00}}, //
		{cmdSetStartLine, []byte{0x00}},     //
		{cmdSetRemap, []byte{0x14, 0x11}},   // horiz addr incr + dual COM
		{cmdSetGPIO, []byte{0x00}},          //
		{cmdFunctionSelect, []byte{0x01}},   // internal VDD regulator
		{cmdDisplayEnhanceA, []byte{0xA0, 0xFD}},
		{cmdSetContrast, []byte{0x9F}},
		{cmdMasterContrast, []byte{0x0F}},
		{cmdSetPhaseLength, []byte{0xE2}},
		{cmdDisplayEnhanceB, []byte{0xA2, 0x20}},
		{cmdSetPrechargeVolt, []byte{0x1F}},
		{cmdSecondPrecharge, []byte{0x08}},
		{cmdSetVCOMH, []byte{0x07}},
		{cmdSetDisplayNormal, nil},
		{cmdExitPartial, nil},
		{cmdDisplayOn, nil},
	}
	for _, s := range seq {
		if err := d.t.Command(s.cmd, s.args...); err != nil {
			return err
		}
	}
	return nil
}
