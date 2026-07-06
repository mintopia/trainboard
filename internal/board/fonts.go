package board

import "github.com/mintopia/trainboard/internal/render"

// Fonts is the loaded font set used by all scenes, mirroring the reference
// board: regular/bold/boldtall at 10px, boldlarge at 20px.
type Fonts struct {
	Regular, Bold, BoldTall, BoldLarge *render.Font
}

// LoadFonts rasterizes the embedded Dot Matrix faces at reference sizes.
func LoadFonts() (*Fonts, error) {
	reg, err := render.LoadFont(render.RegularTTF, 10)
	if err != nil {
		return nil, err
	}
	bold, err := render.LoadFont(render.BoldTTF, 10)
	if err != nil {
		return nil, err
	}
	tall, err := render.LoadFont(render.BoldTallTTF, 10)
	if err != nil {
		return nil, err
	}
	large, err := render.LoadFont(render.BoldTTF, 20)
	if err != nil {
		return nil, err
	}
	return &Fonts{Regular: reg, Bold: bold, BoldTall: tall, BoldLarge: large}, nil
}
