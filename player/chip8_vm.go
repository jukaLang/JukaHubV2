package main

// chip8_vm.go — the CHIP-8 machine JukaHub plays CHIP-8 games in.
//
// CHIP-8 is a tiny 1977 bytecode for the COSMAC VIP: 35 two-byte
// instructions, sixteen 8-bit registers, a 64×32 one-bit screen drawn by
// XOR-ing sprites and a sixteen-key hex keypad. Two extensions matter today:
// SUPER-CHIP 1.1 (HP 48, 1991) adds a 128×64 mode, scrolling, a big font and
// eight "RPL user flags" a game can keep, and XO-CHIP (John Earnest's Octo,
// 2014) adds a second bitplane (four colours), 64 KB of memory, a 16-bit
// index load, register range save/load and a programmable 1-bit audio
// pattern. The homebrew scene (the yearly Octojam, the chip8Archive) writes
// for all three.
//
// The whole machine is a few hundred lines of Go and needs nothing from the
// device: no core, no download, the same on every TrimUI and on Windows. It
// runs on the main thread inside the player scene (chip8_scene.go), a frame at
// a time: tickrate instructions, then the two 60 Hz timers count down.
//
// The hard part of CHIP-8 is not the instructions but their disagreements:
// the VIP, SUPER-CHIP and Octo each do a few of them differently, and a game
// is written for one. Octo's seven quirk switches name those differences, and
// the chip8Archive records the ones each game wants, so the machine takes the
// same switches (chip8Quirks) and the options code (chip8_options.go) picks
// them per game:
//
//   - shiftQuirks: 8XY6/8XYE shift VX itself (SUPER-CHIP), not VY into VX.
//   - loadStoreQuirks: FX55/FX65 leave I alone (SUPER-CHIP) instead of
//     moving it past the registers.
//   - vfOrderQuirks: an arithmetic result written to VF wins over the flag.
//   - clipQuirks: sprites are cut at the screen's edges instead of wrapping.
//   - jumpQuirks: BNNN is BXNN, a jump to XNN + VX.
//   - logicQuirks: 8XY1/2/3 clear VF (the VIP's ALU did).
//   - vBlankQuirks: a sprite draw waits for the next frame (the VIP's
//     display wait), so at most one draw per frame.
//
// Everything else follows Octo, the reference most homebrew is tested in:
// a resolution switch clears the screen, scrolling moves the current
// resolution's pixels, FX0A takes the key when it is let go, and a skip over
// XO-CHIP's four-byte F000 NNNN skips all four bytes. A program that runs
// into an instruction no CHIP-8 has, or overflows its stack, stops with a
// plain message rather than running on as noise.

import "fmt"

const (
	chip8MemSize   = 0x10000 // XO-CHIP's 64 KB; CHIP-8 programs use the first 4 KB
	chip8ProgStart = 0x200
	chip8MaxW      = 128
	chip8MaxH      = 64
	chip8StackMax  = 16
	chip8FontSmall = 0x000 // 16 glyphs × 5 bytes
	chip8FontBig   = 0x050 // 16 glyphs × 10 bytes
	// chip8MaxProgram is the largest program that fits above 0x200.
	chip8MaxProgram = chip8MemSize - chip8ProgStart
	// chip8MaxTickrate bounds instructions per frame (Octo's own top speed
	// is in the thousands; 10000 is 600 000 a second, a few ms on an A53).
	chip8MaxTickrate = 10000
)

// chip8Quirks are Octo's quirk switches (see the file comment).
type chip8Quirks struct {
	Shift     bool `json:"shift"`
	LoadStore bool `json:"loadStore"`
	VfOrder   bool `json:"vfOrder"`
	Clip      bool `json:"clip"`
	Jump      bool `json:"jump"`
	Logic     bool `json:"logic"`
	VBlank    bool `json:"vBlank"`
}

// chip8FontSmallData is the 4×5 hex font every CHIP-8 has.
var chip8FontSmallData = [16 * 5]byte{
	0xF0, 0x90, 0x90, 0x90, 0xF0, 0x20, 0x60, 0x20, 0x20, 0x70,
	0xF0, 0x10, 0xF0, 0x80, 0xF0, 0xF0, 0x10, 0xF0, 0x10, 0xF0,
	0x90, 0x90, 0xF0, 0x10, 0x10, 0xF0, 0x80, 0xF0, 0x10, 0xF0,
	0xF0, 0x80, 0xF0, 0x90, 0xF0, 0xF0, 0x10, 0x20, 0x40, 0x40,
	0xF0, 0x90, 0xF0, 0x90, 0xF0, 0xF0, 0x90, 0xF0, 0x10, 0xF0,
	0xF0, 0x90, 0xF0, 0x90, 0x90, 0xE0, 0x90, 0xE0, 0x90, 0xE0,
	0xF0, 0x80, 0x80, 0x80, 0xF0, 0xE0, 0x90, 0x90, 0x90, 0xE0,
	0xF0, 0x80, 0xF0, 0x80, 0xF0, 0xF0, 0x80, 0xF0, 0x80, 0x80,
}

// chip8FontBigData is the 8×10 font FX30 points at: SUPER-CHIP's digits and
// Octo's A–F.
var chip8FontBigData = [16 * 10]byte{
	0xFF, 0xFF, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xFF, 0xFF,
	0x18, 0x78, 0x78, 0x18, 0x18, 0x18, 0x18, 0x18, 0xFF, 0xFF,
	0xFF, 0xFF, 0x03, 0x03, 0xFF, 0xFF, 0xC0, 0xC0, 0xFF, 0xFF,
	0xFF, 0xFF, 0x03, 0x03, 0xFF, 0xFF, 0x03, 0x03, 0xFF, 0xFF,
	0xC3, 0xC3, 0xC3, 0xC3, 0xFF, 0xFF, 0x03, 0x03, 0x03, 0x03,
	0xFF, 0xFF, 0xC0, 0xC0, 0xFF, 0xFF, 0x03, 0x03, 0xFF, 0xFF,
	0xFF, 0xFF, 0xC0, 0xC0, 0xFF, 0xFF, 0xC3, 0xC3, 0xFF, 0xFF,
	0xFF, 0xFF, 0x03, 0x03, 0x06, 0x0C, 0x18, 0x18, 0x18, 0x18,
	0xFF, 0xFF, 0xC3, 0xC3, 0xFF, 0xFF, 0xC3, 0xC3, 0xFF, 0xFF,
	0xFF, 0xFF, 0xC3, 0xC3, 0xFF, 0xFF, 0x03, 0x03, 0xFF, 0xFF,
	0x7E, 0xFF, 0xC3, 0xC3, 0xC3, 0xFF, 0xFF, 0xC3, 0xC3, 0xC3,
	0xFC, 0xFC, 0xC3, 0xC3, 0xFC, 0xFC, 0xC3, 0xC3, 0xFC, 0xFC,
	0x3C, 0xFF, 0xC3, 0xC0, 0xC0, 0xC0, 0xC0, 0xC3, 0xFF, 0x3C,
	0xFC, 0xFE, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xC3, 0xFE, 0xFC,
	0xFF, 0xFF, 0xC0, 0xC0, 0xFF, 0xFF, 0xC0, 0xC0, 0xFF, 0xFF,
	0xFF, 0xFF, 0xC0, 0xC0, 0xFF, 0xFF, 0xC0, 0xC0, 0xC0, 0xC0,
}

// chip8VM is one machine. Not safe for concurrent use: the player scene owns
// it on the main thread (a headless cover run owns its own copy).
type chip8VM struct {
	mem    [chip8MemSize]byte
	v      [16]byte
	i      uint16
	pc     uint16
	stack  [chip8StackMax]uint16
	sp     int
	dt, st byte
	hires  bool
	planes byte // XO-CHIP's selected bitplanes (bit 0 plane 1, bit 1 plane 2)
	// disp holds both planes, one byte per pixel, with a stride of 128 in
	// both resolutions (lores uses the top-left 64×32).
	disp [2][chip8MaxW * chip8MaxH]byte
	// flags are SUPER-CHIP's RPL user flags (XO-CHIP has sixteen); the
	// player keeps them per game (chip8_library.go). xo allows all sixteen:
	// SUPER-CHIP's FX75/FX85 reach v0..v7 only, so a larger X is cut to 7.
	flags      [16]byte
	flagsDirty bool
	xo         bool
	keys       [16]bool
	// FX0A: waiting for a key, the register it goes to, and the key pressed
	// during the wait (-1 none) that is stored when it is let go.
	waiting bool
	waitReg byte
	waitKey int
	// XO-CHIP audio: the 16-byte pattern (F002), its pitch (FX3A) and whether
	// the program ever set one (otherwise the beeper is a plain tone).
	pattern    [16]byte
	pitch      byte
	patternSet bool
	quirks     chip8Quirks
	// halted is a program stopped by an error (why says what), exited one
	// that ran 00FD.
	halted bool
	exited bool
	why    string
	// dirty is a change to the screen since the player last drew it.
	dirty bool
	rng   uint32
}

// newChip8VM is a machine with program loaded at 0x200. A program too big for
// memory is cut (the caller has already refused files that large).
func newChip8VM(program []byte, quirks chip8Quirks, seed uint32) *chip8VM {
	m := &chip8VM{quirks: quirks}
	m.reset(program, seed)
	return m
}

// reset powers the machine on again with program; the RPL flags survive, as
// they did in the calculator.
func (m *chip8VM) reset(program []byte, seed uint32) {
	flags := m.flags
	quirks, xo := m.quirks, m.xo
	*m = chip8VM{quirks: quirks, flags: flags, xo: xo}
	copy(m.mem[chip8FontSmall:], chip8FontSmallData[:])
	copy(m.mem[chip8FontBig:], chip8FontBigData[:])
	if len(program) > chip8MaxProgram {
		program = program[:chip8MaxProgram]
	}
	copy(m.mem[chip8ProgStart:], program)
	m.pc = chip8ProgStart
	m.planes = 1
	m.pitch = 64
	m.waitKey = -1
	m.dirty = true
	if seed == 0 {
		seed = 0x9E3779B9
	}
	m.rng = seed
}

// size is the current resolution.
func (m *chip8VM) size() (w, h int) {
	if m.hires {
		return 128, 64
	}
	return 64, 32
}

// setKeys hands the machine the keypad as it is this frame, and finishes an
// FX0A wait on the release of a key pressed during it.
func (m *chip8VM) setKeys(keys [16]bool) {
	prev := m.keys
	m.keys = keys
	if !m.waiting {
		return
	}
	if m.waitKey < 0 {
		for k := range 16 {
			if keys[k] && !prev[k] {
				m.waitKey = k
				break
			}
		}
		return
	}
	if !keys[m.waitKey] {
		m.v[m.waitReg&0xF] = byte(m.waitKey)
		m.waiting = false
		m.waitKey = -1
	}
}

// runFrame runs one 60 Hz frame: up to tickrate instructions (fewer when the
// program waits for a key, stops, or draws under the display wait), then the
// timers.
func (m *chip8VM) runFrame(tickrate int) {
	if m.halted || m.exited {
		return
	}
	tickrate = max(1, min(tickrate, chip8MaxTickrate))
	for range tickrate {
		if m.waiting {
			break
		}
		drew := m.step()
		if m.halted || m.exited {
			break
		}
		if drew && m.quirks.VBlank {
			break
		}
	}
	if m.dt > 0 {
		m.dt--
	}
	if m.st > 0 {
		m.st--
	}
}

// soundOn reports whether the buzzer sounds this frame.
func (m *chip8VM) soundOn() bool { return m.st > 0 && !m.halted && !m.exited }

// stop halts the machine with a message for the player.
func (m *chip8VM) stop(format string, args ...any) {
	m.halted = true
	m.why = fmt.Sprintf(format, args...)
}

func (m *chip8VM) word(addr uint16) uint16 {
	return uint16(m.mem[addr])<<8 | uint16(m.mem[addr+1])
}

// skip steps over the next instruction, all four bytes of an F000 NNNN.
func (m *chip8VM) skip() {
	if m.word(m.pc) == 0xF000 {
		m.pc += 4
		return
	}
	m.pc += 2
}

func (m *chip8VM) random() byte {
	x := m.rng
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	m.rng = x
	return byte(x >> 8)
}

// writeCarry stores an arithmetic result and its flag in Octo's order.
func (m *chip8VM) writeCarry(dest byte, value byte, flag bool) {
	m.v[dest] = value
	if flag {
		m.v[0xF] = 1
	} else {
		m.v[0xF] = 0
	}
	if m.quirks.VfOrder {
		m.v[dest] = value
	}
}

// step runs one instruction and reports whether it drew a sprite.
func (m *chip8VM) step() (drew bool) {
	at := m.pc
	op := m.word(at)
	m.pc += 2
	x := byte(op>>8) & 0xF
	y := byte(op>>4) & 0xF
	n := byte(op) & 0xF
	nn := byte(op)
	nnn := op & 0xFFF
	switch op >> 12 {
	case 0x0:
		switch {
		case op == 0x00E0:
			m.clear()
		case op == 0x00EE:
			if m.sp == 0 {
				m.stop("Stopped at %03X: a return with nothing to return to", at)
				return false
			}
			m.sp--
			m.pc = m.stack[m.sp]
		case op&0xFFF0 == 0x00C0:
			m.scrollDown(int(n))
		case op&0xFFF0 == 0x00D0:
			m.scrollDown(-int(n))
		case op == 0x00FB:
			m.scrollRight(4)
		case op == 0x00FC:
			m.scrollRight(-4)
		case op == 0x00FD:
			m.exited = true
		case op == 0x00FE, op == 0x00FF:
			m.hires = op == 0x00FF
			m.disp = [2][chip8MaxW * chip8MaxH]byte{}
			m.dirty = true
		case op == 0x0000:
			m.stop("Stopped at %03X: the program ran into empty memory", at)
		default:
			// 0NNN called machine code of the VIP's own 1802 CPU.
			m.stop("Stopped at %03X: %04X calls VIP machine code, which no player runs", at, op)
		}
	case 0x1:
		m.pc = nnn
	case 0x2:
		if m.sp >= chip8StackMax {
			m.stop("Stopped at %03X: too many nested calls (stack overflow)", at)
			return false
		}
		m.stack[m.sp] = m.pc
		m.sp++
		m.pc = nnn
	case 0x3:
		if m.v[x] == nn {
			m.skip()
		}
	case 0x4:
		if m.v[x] != nn {
			m.skip()
		}
	case 0x5:
		switch n {
		case 0x0:
			if m.v[x] == m.v[y] {
				m.skip()
			}
		case 0x2: // XO-CHIP: save vx..vy at I
			m.rangeCopy(x, y, true)
		case 0x3: // XO-CHIP: load vx..vy from I
			m.rangeCopy(x, y, false)
		default:
			m.stop("Stopped at %03X: unknown instruction %04X", at, op)
		}
	case 0x6:
		m.v[x] = nn
	case 0x7:
		m.v[x] += nn
	case 0x8:
		vx, vy := m.v[x], m.v[y]
		switch n {
		case 0x0:
			m.v[x] = vy
		case 0x1:
			m.v[x] = vx | vy
			if m.quirks.Logic {
				m.v[0xF] = 0
			}
		case 0x2:
			m.v[x] = vx & vy
			if m.quirks.Logic {
				m.v[0xF] = 0
			}
		case 0x3:
			m.v[x] = vx ^ vy
			if m.quirks.Logic {
				m.v[0xF] = 0
			}
		case 0x4:
			sum := uint16(vx) + uint16(vy)
			m.writeCarry(x, byte(sum), sum > 0xFF)
		case 0x5:
			m.writeCarry(x, vx-vy, vx >= vy)
		case 0x7:
			m.writeCarry(x, vy-vx, vy >= vx)
		case 0x6:
			src := vy
			if m.quirks.Shift {
				src = vx
			}
			m.writeCarry(x, src>>1, src&1 != 0)
		case 0xE:
			src := vy
			if m.quirks.Shift {
				src = vx
			}
			m.writeCarry(x, src<<1, src&0x80 != 0)
		default:
			m.stop("Stopped at %03X: unknown instruction %04X", at, op)
		}
	case 0x9:
		if m.v[x] != m.v[y] {
			m.skip()
		}
	case 0xA:
		m.i = nnn
	case 0xB:
		if m.quirks.Jump {
			m.pc = nnn + uint16(m.v[x])
		} else {
			m.pc = nnn + uint16(m.v[0])
		}
	case 0xC:
		m.v[x] = m.random() & nn
	case 0xD:
		m.draw(m.v[x], m.v[y], n)
		return true
	case 0xE:
		key := m.v[x] & 0xF
		switch nn {
		case 0x9E:
			if m.keys[key] {
				m.skip()
			}
		case 0xA1:
			if !m.keys[key] {
				m.skip()
			}
		default:
			m.stop("Stopped at %03X: unknown instruction %04X", at, op)
		}
	case 0xF:
		m.stepF(at, op, x, nn)
	}
	return false
}

// stepF runs the FXNN group.
func (m *chip8VM) stepF(at, op uint16, x, nn byte) {
	switch {
	case op == 0xF000: // XO-CHIP: I = the next 16-bit word
		m.i = m.word(m.pc)
		m.pc += 2
	case op == 0xF002: // XO-CHIP: the audio pattern from I
		for k := range 16 {
			m.pattern[k] = m.mem[m.i+uint16(k)]
		}
		m.patternSet = true
	case nn == 0x01: // XO-CHIP: FN01 selects the bitplanes
		m.planes = x & 3
	case nn == 0x07:
		m.v[x] = m.dt
	case nn == 0x0A:
		m.waiting = true
		m.waitReg = x
		m.waitKey = -1
	case nn == 0x15:
		m.dt = m.v[x]
	case nn == 0x18:
		m.st = m.v[x]
	case nn == 0x1E:
		m.i += uint16(m.v[x])
	case nn == 0x29:
		m.i = chip8FontSmall + uint16(m.v[x]&0xF)*5
	case nn == 0x30:
		m.i = chip8FontBig + uint16(m.v[x]&0xF)*10
	case nn == 0x33:
		v := m.v[x]
		m.mem[m.i] = v / 100
		m.mem[m.i+1] = (v / 10) % 10
		m.mem[m.i+2] = v % 10
	case nn == 0x3A: // XO-CHIP: the pattern's pitch
		m.pitch = m.v[x]
	case nn == 0x55:
		for k := uint16(0); k <= uint16(x); k++ {
			m.mem[m.i+k] = m.v[k]
		}
		if !m.quirks.LoadStore {
			m.i += uint16(x) + 1
		}
	case nn == 0x65:
		for k := uint16(0); k <= uint16(x); k++ {
			m.v[k] = m.mem[m.i+k]
		}
		if !m.quirks.LoadStore {
			m.i += uint16(x) + 1
		}
	case nn == 0x75: // SUPER-CHIP: save v0..vx to the RPL flags
		for k := 0; k <= m.flagTop(x); k++ {
			if m.flags[k] != m.v[k] {
				m.flags[k] = m.v[k]
				m.flagsDirty = true
			}
		}
	case nn == 0x85:
		for k := 0; k <= m.flagTop(x); k++ {
			m.v[k] = m.flags[k]
		}
	default:
		m.stop("Stopped at %03X: unknown instruction %04X", at, op)
	}
}

// flagTop is the last register FX75/FX85 move: X, at most v7 outside XO-CHIP
// (the HP 48 had eight RPL user flags).
func (m *chip8VM) flagTop(x byte) int {
	if !m.xo {
		return min(int(x), 7)
	}
	return int(x & 0xF)
}

// rangeCopy is XO-CHIP's 5XY2 (save) and 5XY3 (load): vx through vy at I,
// in reverse when x > y; I does not move.
func (m *chip8VM) rangeCopy(x, y byte, save bool) {
	dist := int(x) - int(y)
	step := -1
	if dist < 0 {
		dist, step = -dist, 1
	}
	for k := 0; k <= dist; k++ {
		r := int(x) + k*step
		addr := m.i + uint16(k)
		if save {
			m.mem[addr] = m.v[r]
		} else {
			m.v[r] = m.mem[addr]
		}
	}
}

// clear blanks the selected planes.
func (m *chip8VM) clear() {
	for p := range 2 {
		if m.planes&(1<<p) != 0 {
			m.disp[p] = [chip8MaxW * chip8MaxH]byte{}
		}
	}
	m.dirty = true
}

// draw is DXYN: an 8×N sprite (16×16 for N = 0) XOR-ed onto each selected
// plane in turn, plane 2's rows following plane 1's in memory. VF reports
// whether any lit pixel went out.
func (m *chip8VM) draw(vx, vy, n byte) {
	w, h := m.size()
	rows, cols, bytesPerRow := int(n), 8, 1
	if n == 0 {
		rows, cols, bytesPerRow = 16, 16, 2
	}
	x0, y0 := int(vx)%w, int(vy)%h
	collide := false
	addr := m.i
	for p := range 2 {
		if m.planes&(1<<p) == 0 {
			continue
		}
		plane := &m.disp[p]
		for r := range rows {
			line := uint16(m.mem[addr+uint16(r*bytesPerRow)])
			if bytesPerRow == 2 {
				line = line<<8 | uint16(m.mem[addr+uint16(r*2+1)])
			}
			py := y0 + r
			if py >= h {
				if m.quirks.Clip {
					break
				}
				py %= h
			}
			for c := range cols {
				if line&(1<<(cols-1-c)) == 0 {
					continue
				}
				px := x0 + c
				if px >= w {
					if m.quirks.Clip {
						break
					}
					px %= w
				}
				idx := py*chip8MaxW + px
				if plane[idx] != 0 {
					collide = true
				}
				plane[idx] ^= 1
			}
		}
		addr += uint16(rows * bytesPerRow)
	}
	if collide {
		m.v[0xF] = 1
	} else {
		m.v[0xF] = 0
	}
	m.dirty = true
}

// scrollDown moves the selected planes down n rows (up for a negative n).
func (m *chip8VM) scrollDown(n int) {
	if n == 0 {
		return
	}
	w, h := m.size()
	for p := range 2 {
		if m.planes&(1<<p) == 0 {
			continue
		}
		plane := &m.disp[p]
		if n > 0 {
			for y := h - 1; y >= 0; y-- {
				for x := range w {
					var px byte
					if y-n >= 0 {
						px = plane[(y-n)*chip8MaxW+x]
					}
					plane[y*chip8MaxW+x] = px
				}
			}
		} else {
			for y := range h {
				for x := range w {
					var px byte
					if y-n < h {
						px = plane[(y-n)*chip8MaxW+x]
					}
					plane[y*chip8MaxW+x] = px
				}
			}
		}
	}
	m.dirty = true
}

// scrollRight moves the selected planes right n pixels (left for negative).
func (m *chip8VM) scrollRight(n int) {
	w, h := m.size()
	for p := range 2 {
		if m.planes&(1<<p) == 0 {
			continue
		}
		plane := &m.disp[p]
		for y := range h {
			row := plane[y*chip8MaxW : y*chip8MaxW+w]
			if n > 0 {
				for x := w - 1; x >= 0; x-- {
					var px byte
					if x-n >= 0 {
						px = row[x-n]
					}
					row[x] = px
				}
			} else {
				for x := range w {
					var px byte
					if x-n < w {
						px = row[x-n]
					}
					row[x] = px
				}
			}
		}
	}
	m.dirty = true
}

// pixel is the colour index at (x, y): 0 off, 1 plane 1, 2 plane 2, 3 both.
func (m *chip8VM) pixel(x, y int) int {
	idx := y*chip8MaxW + x
	return int(m.disp[0][idx]) | int(m.disp[1][idx])<<1
}

// blank reports whether nothing at all is lit.
func (m *chip8VM) blank() bool {
	for p := range 2 {
		for _, px := range m.disp[p][:] {
			if px != 0 {
				return false
			}
		}
	}
	return true
}

// ── Quick save ───────────────────────────────────────────────────────────────

// chip8StateVersion is the save format; a save of another version is refused.
const chip8StateVersion = 1

// chip8State is a whole machine as the quick save writes it (JSON; memory
// and the screen as base64).
type chip8State struct {
	Version    int      `json:"version"`
	Mem        []byte   `json:"mem"`
	V          []byte   `json:"v"`
	I          uint16   `json:"i"`
	PC         uint16   `json:"pc"`
	Stack      []uint16 `json:"stack"`
	SP         int      `json:"sp"`
	DT         byte     `json:"dt"`
	ST         byte     `json:"st"`
	Hires      bool     `json:"hires"`
	Planes     byte     `json:"planes"`
	Plane1     []byte   `json:"plane1"`
	Plane2     []byte   `json:"plane2"`
	Waiting    bool     `json:"waiting"`
	WaitReg    byte     `json:"waitReg"`
	Pattern    []byte   `json:"pattern"`
	Pitch      byte     `json:"pitch"`
	PatternSet bool     `json:"patternSet"`
	Exited     bool     `json:"exited"`
	RNG        uint32   `json:"rng"`
}

// snapshot copies the machine into a save.
func (m *chip8VM) snapshot() chip8State {
	return chip8State{
		Version:    chip8StateVersion,
		Mem:        append([]byte(nil), m.mem[:]...),
		V:          append([]byte(nil), m.v[:]...),
		I:          m.i,
		PC:         m.pc,
		Stack:      append([]uint16(nil), m.stack[:]...),
		SP:         m.sp,
		DT:         m.dt,
		ST:         m.st,
		Hires:      m.hires,
		Planes:     m.planes,
		Plane1:     append([]byte(nil), m.disp[0][:]...),
		Plane2:     append([]byte(nil), m.disp[1][:]...),
		Waiting:    m.waiting,
		WaitReg:    m.waitReg,
		Pattern:    append([]byte(nil), m.pattern[:]...),
		Pitch:      m.pitch,
		PatternSet: m.patternSet,
		Exited:     m.exited,
		RNG:        m.rng,
	}
}

// restore puts a save back. A save of the wrong shape changes nothing.
func (m *chip8VM) restore(s chip8State) error {
	if s.Version != chip8StateVersion {
		return fmt.Errorf("this save is from another version of the player")
	}
	if len(s.Mem) != chip8MemSize || len(s.V) != 16 || len(s.Stack) != chip8StackMax ||
		len(s.Plane1) != chip8MaxW*chip8MaxH || len(s.Plane2) != chip8MaxW*chip8MaxH ||
		len(s.Pattern) != 16 || s.SP < 0 || s.SP > chip8StackMax {
		return fmt.Errorf("this save is damaged")
	}
	copy(m.mem[:], s.Mem)
	copy(m.v[:], s.V)
	m.i, m.pc = s.I, s.PC
	copy(m.stack[:], s.Stack)
	m.sp = s.SP
	m.dt, m.st = s.DT, s.ST
	m.hires, m.planes = s.Hires, s.Planes&3
	copy(m.disp[0][:], s.Plane1)
	copy(m.disp[1][:], s.Plane2)
	m.waiting, m.waitReg, m.waitKey = s.Waiting, s.WaitReg&0xF, -1
	copy(m.pattern[:], s.Pattern)
	m.pitch, m.patternSet = s.Pitch, s.PatternSet
	m.exited = s.Exited
	m.halted, m.why = false, ""
	m.rng = s.RNG
	if m.rng == 0 {
		m.rng = 0x9E3779B9
	}
	m.dirty = true
	return nil
}
