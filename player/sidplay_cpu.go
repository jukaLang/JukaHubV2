package main

// sidplay_cpu.go — the 6510 that runs a SID tune's init and play routines.
//
// A plain NMOS 6502 interpreter (the 6510 adds only the I/O port at $00/$01)
// with the undocumented opcodes C64 music drivers use: LAX, SAX, DCP, ISC,
// SLO, RLA, SRE, RRA, ANC, ALR, ARR, SBX, the multi-byte NOPs and the unstable
// SHA/SHX/SHY/TAS/LAS/ANE/LXA in their common forms. Decimal mode follows the
// NMOS flags. Cycle counts come from the standard table without page-cross
// penalties: they only pace the raster and CIA reads and bound a call.
//
// Memory is 64 KB of RAM. With I/O banked in ($01), $D400-$D7FF is the SID
// (sidplay_chip.go), $D011/$D012 read a raster line derived from the cycle
// count and $DC04/$DC05 keep the CIA timer latch a tune sets for its speed.
// There is no BASIC or KERNAL ROM: the KERNAL's interrupt exit at $EA31/$EA81
// is stubbed in RAM because interrupt-driven tunes jump to it, and a BRK or a
// JAM ends the call instead of crashing into nothing.

// sidCycleTable is the 6502's cycles per opcode (illegal ones included).
var sidCycleTable = [256]uint8{
	7, 6, 2, 8, 3, 3, 5, 5, 3, 2, 2, 2, 4, 4, 6, 6,
	2, 5, 2, 8, 4, 4, 6, 6, 2, 4, 2, 7, 4, 4, 7, 7,
	6, 6, 2, 8, 3, 3, 5, 5, 4, 2, 2, 2, 4, 4, 6, 6,
	2, 5, 2, 8, 4, 4, 6, 6, 2, 4, 2, 7, 4, 4, 7, 7,
	6, 6, 2, 8, 3, 3, 5, 5, 3, 2, 2, 2, 3, 4, 6, 6,
	2, 5, 2, 8, 4, 4, 6, 6, 2, 4, 2, 7, 4, 4, 7, 7,
	6, 6, 2, 8, 3, 3, 5, 5, 4, 2, 2, 2, 5, 4, 6, 6,
	2, 5, 2, 8, 4, 4, 6, 6, 2, 4, 2, 7, 4, 4, 7, 7,
	2, 6, 2, 6, 3, 3, 3, 3, 2, 2, 2, 2, 4, 4, 4, 4,
	2, 6, 2, 6, 4, 4, 4, 4, 2, 5, 2, 5, 5, 5, 5, 5,
	2, 6, 2, 6, 3, 3, 3, 3, 2, 2, 2, 2, 4, 4, 4, 4,
	2, 5, 2, 5, 4, 4, 4, 4, 2, 4, 2, 4, 4, 4, 4, 4,
	2, 6, 2, 8, 3, 3, 5, 5, 2, 2, 2, 2, 4, 4, 6, 6,
	2, 5, 2, 8, 4, 4, 6, 6, 2, 4, 2, 7, 4, 4, 7, 7,
	2, 6, 2, 8, 3, 3, 5, 5, 2, 2, 2, 2, 4, 4, 6, 6,
	2, 5, 2, 8, 4, 4, 6, 6, 2, 4, 2, 7, 4, 4, 7, 7,
}

const (
	sidFlagC = 0x01
	sidFlagZ = 0x02
	sidFlagI = 0x04
	sidFlagD = 0x08
	sidFlagB = 0x10
	sidFlagU = 0x20
	sidFlagV = 0x40
	sidFlagN = 0x80
)

// sidMachine is the C64 as far as a PSID tune sees it.
type sidMachine struct {
	ram      [0x10000]byte
	a, x, y  byte
	s, p     byte
	pc       uint16
	cycles   int64
	ciaLatch uint16
	chip     sidChip
	halted   bool
	// extraSID are a 2SID/3SID tune's other chips (sidTune.ExtraSID): this
	// player has one chip, and their writes must not reach it through its
	// mirrors at $D420-$D7FF.
	extraSID [2]uint16
}

// otherSID reports an address of the tune's second or third chip.
func (m *sidMachine) otherSID(addr uint16) bool {
	base := addr &^ 0x1F
	return base == m.extraSID[0] || base == m.extraSID[1]
}

// loadKernalStubs puts the KERNAL's interrupt exit in RAM: $EA31 jumps to
// $EA81, which pulls Y, X and A and returns from the interrupt. The tune's own
// data, loaded afterwards, wins where it overlaps.
func (m *sidMachine) loadKernalStubs() {
	copy(m.ram[0xEA31:], []byte{0x4C, 0x81, 0xEA})
	copy(m.ram[0xEA81:], []byte{0x68, 0xA8, 0x68, 0xAA, 0x68, 0x40})
	// The hardware vectors point at the stub too, so a stray BRK or IRQ
	// returns instead of running zeroes.
	m.ram[0xFFFE], m.ram[0xFFFF] = 0x81, 0xEA
	m.ram[0x0314], m.ram[0x0315] = 0x31, 0xEA
}

// io reports whether $D000-$DFFF shows the chips (CHAREN set and either ROM
// line on) rather than RAM.
func (m *sidMachine) io() bool {
	port := m.ram[1]
	return port&4 != 0 && port&3 != 0
}

func (m *sidMachine) read(addr uint16) byte {
	if addr >= 0xD000 && addr < 0xE000 && m.io() {
		switch {
		case addr >= 0xD400 && addr < 0xD800 && !m.otherSID(addr):
			return m.chip.read(byte(addr & 0x1F))
		case addr == 0xD012:
			return byte(m.cycles / 63 % 312)
		case addr == 0xD011:
			return m.ram[addr]&0x7F | byte(m.cycles/63%312>>8)<<7
		case addr == 0xDC04:
			return byte(m.ciaLatch)
		case addr == 0xDC05:
			return byte(m.ciaLatch >> 8)
		}
	}
	return m.ram[addr]
}

func (m *sidMachine) write(addr uint16, v byte) {
	if addr >= 0xD000 && addr < 0xE000 && m.io() {
		switch {
		case addr >= 0xD400 && addr < 0xD800 && !m.otherSID(addr):
			m.chip.write(byte(addr&0x1F), v)
			return
		case addr == 0xDC04:
			m.ciaLatch = m.ciaLatch&0xFF00 | uint16(v)
		case addr == 0xDC05:
			m.ciaLatch = m.ciaLatch&0x00FF | uint16(v)<<8
		}
	}
	m.ram[addr] = v
}

func (m *sidMachine) push(v byte) {
	m.ram[0x100|uint16(m.s)] = v
	m.s--
}

func (m *sidMachine) pull() byte {
	m.s++
	return m.ram[0x100|uint16(m.s)]
}

func (m *sidMachine) read16(addr uint16) uint16 {
	return uint16(m.read(addr)) | uint16(m.read(addr+1))<<8
}

// read16zp reads a pointer from the zero page, wrapping inside it.
func (m *sidMachine) read16zp(zp byte) uint16 {
	return uint16(m.ram[zp]) | uint16(m.ram[byte(zp+1)])<<8
}

// call runs a routine as JSR would and returns when it returns (the return
// address is $0000 - 1, so its RTS lands on $0000, which never holds code), or
// when budget cycles have run out.
func (m *sidMachine) call(addr uint16, a byte, budget int) {
	m.s = 0xFF
	m.push(0xFF)
	m.push(0xFF)
	m.a, m.x, m.y = a, 0, 0
	m.p = sidFlagU | sidFlagI
	m.pc = addr
	m.run(budget)
}

// interrupt calls the tune's own interrupt handler (play address 0): through
// $0314 the way the KERNAL's IRQ entry does when the KERNAL is banked in,
// else through the hardware vector at $FFFE. The pushed return address is
// $0000, so the handler's RTI ends the call.
func (m *sidMachine) interrupt(budget int) {
	m.s = 0xFF
	m.push(0x00)
	m.push(0x00)
	m.push(m.p&^sidFlagB | sidFlagU)
	m.p |= sidFlagI
	if m.ram[1]&2 != 0 {
		m.push(m.a)
		m.push(m.x)
		m.push(m.y)
		m.pc = m.read16(0x0314)
	} else {
		m.pc = m.read16(0xFFFE)
	}
	m.run(budget)
}

func (m *sidMachine) run(budget int) {
	m.halted = false
	for budget > 0 && m.pc != 0 && !m.halted {
		c := m.step()
		budget -= c
		m.cycles += int64(c)
	}
}

func (m *sidMachine) setNZ(v byte) {
	m.p &^= sidFlagN | sidFlagZ
	if v == 0 {
		m.p |= sidFlagZ
	}
	m.p |= v & sidFlagN
}

func (m *sidMachine) flag(f byte, on bool) {
	if on {
		m.p |= f
	} else {
		m.p &^= f
	}
}

// ── Addressing ───────────────────────────────────────────────────────────────

func (m *sidMachine) fetch() byte {
	v := m.read(m.pc)
	m.pc++
	return v
}

func (m *sidMachine) fetch16() uint16 {
	v := m.read16(m.pc)
	m.pc += 2
	return v
}

// operand returns the effective address of the ALU-group addressing mode
// bbb (bits 2-4 of the opcode); yIndexed switches zp,X / abs,X to Y (LDX, STX,
// LAX, SAX).
func (m *sidMachine) operand(bbb byte, yIndexed bool) uint16 {
	switch bbb {
	case 0: // (zp,X)
		return m.read16zp(m.fetch() + m.x)
	case 1: // zp
		return uint16(m.fetch())
	case 2: // #imm
		m.pc++
		return m.pc - 1
	case 3: // abs
		return m.fetch16()
	case 4: // (zp),Y
		return m.read16zp(m.fetch()) + uint16(m.y)
	case 5: // zp,X / zp,Y
		if yIndexed {
			return uint16(m.fetch() + m.y)
		}
		return uint16(m.fetch() + m.x)
	case 6: // abs,Y
		return m.fetch16() + uint16(m.y)
	default: // abs,X / abs,Y
		if yIndexed {
			return m.fetch16() + uint16(m.y)
		}
		return m.fetch16() + uint16(m.x)
	}
}

// ── Arithmetic ───────────────────────────────────────────────────────────────

func (m *sidMachine) adc(v byte) {
	c := uint16(m.p & sidFlagC)
	if m.p&sidFlagD != 0 {
		lo := uint16(m.a&0x0F) + uint16(v&0x0F) + c
		hi := uint16(m.a>>4) + uint16(v>>4)
		if lo > 9 {
			lo += 6
		}
		if lo > 0x0F {
			hi++
		}
		bin := uint16(m.a) + uint16(v) + c
		m.flag(sidFlagZ, byte(bin) == 0)
		m.flag(sidFlagN, hi&8 != 0)
		m.flag(sidFlagV, (^(m.a^v))&(m.a^byte(hi<<4))&0x80 != 0)
		if hi > 9 {
			hi += 6
		}
		m.flag(sidFlagC, hi > 0x0F)
		m.a = byte(hi<<4) | byte(lo&0x0F)
		return
	}
	sum := uint16(m.a) + uint16(v) + c
	r := byte(sum)
	m.flag(sidFlagC, sum > 0xFF)
	m.flag(sidFlagV, (^(m.a^v))&(m.a^r)&0x80 != 0)
	m.a = r
	m.setNZ(r)
}

func (m *sidMachine) sbc(v byte) {
	if m.p&sidFlagD != 0 {
		c := int(m.p & sidFlagC)
		diff := int(m.a) - int(v) - (1 - c)
		lo := int(m.a&0x0F) - int(v&0x0F) - (1 - c)
		hi := int(m.a>>4) - int(v>>4)
		if lo < 0 {
			lo -= 6
			hi--
		}
		if hi < 0 {
			hi -= 6
		}
		r := byte(diff)
		m.flag(sidFlagC, diff >= 0)
		m.flag(sidFlagV, (m.a^v)&(m.a^r)&0x80 != 0)
		m.setNZ(r)
		m.a = byte(hi<<4) | byte(lo&0x0F)
		return
	}
	m.adc(^v)
}

func (m *sidMachine) compare(reg, v byte) {
	m.flag(sidFlagC, reg >= v)
	m.setNZ(reg - v)
}

func (m *sidMachine) asl(v byte) byte {
	m.flag(sidFlagC, v&0x80 != 0)
	v <<= 1
	m.setNZ(v)
	return v
}

func (m *sidMachine) lsr(v byte) byte {
	m.flag(sidFlagC, v&1 != 0)
	v >>= 1
	m.setNZ(v)
	return v
}

func (m *sidMachine) rol(v byte) byte {
	c := m.p & sidFlagC
	m.flag(sidFlagC, v&0x80 != 0)
	v = v<<1 | c
	m.setNZ(v)
	return v
}

func (m *sidMachine) ror(v byte) byte {
	c := m.p & sidFlagC
	m.flag(sidFlagC, v&1 != 0)
	v = v>>1 | c<<7
	m.setNZ(v)
	return v
}

func (m *sidMachine) branch(take bool) {
	off := int8(m.fetch())
	if take {
		m.pc = uint16(int32(m.pc) + int32(off))
	}
}

// ── One instruction ──────────────────────────────────────────────────────────

// step runs one instruction and returns its cycles.
func (m *sidMachine) step() int {
	op := m.fetch()
	cyc := int(sidCycleTable[op])
	aaa, bbb := op>>5, op>>2&7
	switch op & 3 {
	case 1: // ORA AND EOR ADC STA LDA CMP SBC
		if op == 0x89 { // NOP #imm
			m.pc++
			return cyc
		}
		addr := m.operand(bbb, false)
		if aaa == 4 {
			m.write(addr, m.a)
			return cyc
		}
		m.alu(aaa, m.read(addr))
		return cyc
	case 3:
		return m.stepIllegal(op, aaa, bbb, cyc)
	case 2:
		return m.stepShift(op, aaa, bbb, cyc)
	}
	return m.stepMisc(op, aaa, bbb, cyc)
}

// alu is the ALU group's operation aaa on v (STA is handled by the caller).
func (m *sidMachine) alu(aaa, v byte) {
	switch aaa {
	case 0:
		m.a |= v
		m.setNZ(m.a)
	case 1:
		m.a &= v
		m.setNZ(m.a)
	case 2:
		m.a ^= v
		m.setNZ(m.a)
	case 3:
		m.adc(v)
	case 5:
		m.a = v
		m.setNZ(v)
	case 6:
		m.compare(m.a, v)
	case 7:
		m.sbc(v)
	}
}

// rmw is the shift group's read-modify-write operation aaa (ASL ROL LSR ROR
// DEC INC) on v.
func (m *sidMachine) rmw(aaa, v byte) byte {
	switch aaa {
	case 0:
		return m.asl(v)
	case 1:
		return m.rol(v)
	case 2:
		return m.lsr(v)
	case 3:
		return m.ror(v)
	case 6:
		v--
	default:
		v++
	}
	m.setNZ(v)
	return v
}

// stepShift is the cc=10 column: shifts, INC/DEC, LDX/STX and the
// transfers and NOPs that share it.
func (m *sidMachine) stepShift(op, aaa, bbb byte, cyc int) int {
	switch bbb {
	case 0: // #imm: LDX; NOP #imm; the rest JAM
		switch aaa {
		case 5:
			m.x = m.fetch()
			m.setNZ(m.x)
		case 4, 6, 7:
			m.pc++
		default:
			m.halted = true
		}
		return cyc
	case 2: // accumulator and register forms
		switch aaa {
		case 0, 1, 2, 3:
			m.a = m.rmw(aaa, m.a)
		case 4: // TXA
			m.a = m.x
			m.setNZ(m.a)
		case 5: // TAX
			m.x = m.a
			m.setNZ(m.x)
		case 6: // DEX
			m.x--
			m.setNZ(m.x)
		}
		return cyc // 0xEA NOP
	case 4: // JAM
		m.halted = true
		return cyc
	case 6: // TXS, TSX; the rest are 1-byte NOPs
		switch aaa {
		case 4:
			m.s = m.x
		case 5:
			m.x = m.s
			m.setNZ(m.x)
		}
		return cyc
	}
	// zp (1), abs (3), zp,X (5), abs,X (7); LDX/STX index with Y.
	addr := m.operand(bbb, aaa == 4 || aaa == 5)
	switch aaa {
	case 4:
		if op == 0x9E { // SHX abs,Y
			m.write(addr, m.x&(byte(addr>>8)+1))
		} else {
			m.write(addr, m.x)
		}
	case 5:
		m.x = m.read(addr)
		m.setNZ(m.x)
	default:
		m.write(addr, m.rmw(aaa, m.read(addr)))
	}
	return cyc
}

// stepMisc is the cc=00 column: flow control, flags, stack, BIT, LDY/STY,
// CPX/CPY and NOPs.
func (m *sidMachine) stepMisc(op, aaa, bbb byte, cyc int) int {
	switch op {
	case 0x00: // BRK ends the call
		m.halted = true
		return cyc
	case 0x20: // JSR
		target := m.fetch16()
		ret := m.pc - 1
		m.push(byte(ret >> 8))
		m.push(byte(ret))
		m.pc = target
		return cyc
	case 0x40: // RTI
		m.p = m.pull()&^sidFlagB | sidFlagU
		lo := m.pull()
		m.pc = uint16(lo) | uint16(m.pull())<<8
		return cyc
	case 0x60: // RTS
		lo := m.pull()
		m.pc = (uint16(lo) | uint16(m.pull())<<8) + 1
		return cyc
	case 0x4C: // JMP abs
		m.pc = m.fetch16()
		return cyc
	case 0x6C: // JMP (ind), with the page-wrap bug
		ptr := m.fetch16()
		hi := ptr&0xFF00 | uint16(byte(ptr)+1)
		m.pc = uint16(m.read(ptr)) | uint16(m.read(hi))<<8
		return cyc
	case 0x08: // PHP
		m.push(m.p | sidFlagB | sidFlagU)
		return cyc
	case 0x28: // PLP
		m.p = m.pull()&^sidFlagB | sidFlagU
		return cyc
	case 0x48: // PHA
		m.push(m.a)
		return cyc
	case 0x68: // PLA
		m.a = m.pull()
		m.setNZ(m.a)
		return cyc
	case 0x88: // DEY
		m.y--
		m.setNZ(m.y)
		return cyc
	case 0xA8: // TAY
		m.y = m.a
		m.setNZ(m.y)
		return cyc
	case 0xC8: // INY
		m.y++
		m.setNZ(m.y)
		return cyc
	case 0xE8: // INX
		m.x++
		m.setNZ(m.x)
		return cyc
	case 0x98: // TYA
		m.a = m.y
		m.setNZ(m.a)
		return cyc
	case 0x18:
		m.p &^= sidFlagC
		return cyc
	case 0x38:
		m.p |= sidFlagC
		return cyc
	case 0x58:
		m.p &^= sidFlagI
		return cyc
	case 0x78:
		m.p |= sidFlagI
		return cyc
	case 0xB8:
		m.p &^= sidFlagV
		return cyc
	case 0xD8:
		m.p &^= sidFlagD
		return cyc
	case 0xF8:
		m.p |= sidFlagD
		return cyc
	}
	if bbb == 4 { // branches: aaa picks the flag and its wanted value
		var set bool
		switch aaa >> 1 {
		case 0:
			set = m.p&sidFlagN != 0
		case 1:
			set = m.p&sidFlagV != 0
		case 2:
			set = m.p&sidFlagC != 0
		default:
			set = m.p&sidFlagZ != 0
		}
		m.branch(set == (aaa&1 != 0))
		return cyc
	}
	// The rest read or write through zp/abs (indexed by X), or take #imm.
	var addr uint16
	switch bbb {
	case 0:
		addr = m.operand(2, false)
	case 1, 3, 5, 7:
		addr = m.operand(bbb, false)
	default:
		return cyc
	}
	switch aaa {
	case 1: // BIT (zp, abs); the indexed forms are NOPs
		if bbb == 1 || bbb == 3 {
			v := m.read(addr)
			m.flag(sidFlagZ, m.a&v == 0)
			m.p = m.p&^(sidFlagN|sidFlagV) | v&(sidFlagN|sidFlagV)
		}
	case 4: // STY; SHY abs,X
		if bbb == 7 {
			m.write(addr, m.y&(byte(addr>>8)+1))
		} else if bbb != 0 {
			m.write(addr, m.y)
		}
	case 5: // LDY
		m.y = m.read(addr)
		m.setNZ(m.y)
	case 6: // CPY (#, zp, abs)
		if bbb <= 3 {
			m.compare(m.y, m.read(addr))
		}
	case 7: // CPX (#, zp, abs)
		if bbb <= 3 {
			m.compare(m.x, m.read(addr))
		}
	}
	return cyc
}

// stepIllegal is the cc=11 column: the undocumented combinations.
func (m *sidMachine) stepIllegal(op, aaa, bbb byte, cyc int) int {
	if bbb == 2 { // the #imm column
		v := m.fetch()
		switch aaa {
		case 0, 1: // ANC
			m.a &= v
			m.setNZ(m.a)
			m.flag(sidFlagC, m.a&0x80 != 0)
		case 2: // ALR
			m.a = m.lsr(m.a & v)
		case 3: // ARR
			m.a &= v
			c := m.p & sidFlagC
			m.a = m.a>>1 | c<<7
			m.setNZ(m.a)
			bit6, bit5 := m.a&0x40 != 0, m.a&0x20 != 0
			m.flag(sidFlagC, bit6)
			m.flag(sidFlagV, bit6 != bit5)
		case 4: // ANE (unstable; the common constant)
			m.a = (m.a | 0xEE) & m.x & v
			m.setNZ(m.a)
		case 5: // LXA
			m.a = (m.a | 0xEE) & v
			m.x = m.a
			m.setNZ(m.a)
		case 6: // SBX
			r := uint16(m.a&m.x) - uint16(v)
			m.flag(sidFlagC, r < 0x100)
			m.x = byte(r)
			m.setNZ(m.x)
		default: // SBC #imm
			m.sbc(v)
		}
		return cyc
	}
	// SAX and LAX index zp and abs with Y.
	addr := m.operand(bbb, aaa == 4 || aaa == 5)
	switch aaa {
	case 4:
		switch op {
		case 0x93, 0x9F: // SHA
			m.write(addr, m.a&m.x&(byte(addr>>8)+1))
		case 0x9B: // TAS
			m.s = m.a & m.x
			m.write(addr, m.s&(byte(addr>>8)+1))
		default: // SAX
			m.write(addr, m.a&m.x)
		}
	case 5:
		v := m.read(addr)
		if op == 0xBB { // LAS
			v &= m.s
			m.s = v
		}
		m.a, m.x = v, v
		m.setNZ(v)
	case 0: // SLO
		v := m.asl(m.read(addr))
		m.write(addr, v)
		m.a |= v
		m.setNZ(m.a)
	case 1: // RLA
		v := m.rol(m.read(addr))
		m.write(addr, v)
		m.a &= v
		m.setNZ(m.a)
	case 2: // SRE
		v := m.lsr(m.read(addr))
		m.write(addr, v)
		m.a ^= v
		m.setNZ(m.a)
	case 3: // RRA
		v := m.ror(m.read(addr))
		m.write(addr, v)
		m.adc(v)
	case 6: // DCP
		v := m.read(addr) - 1
		m.write(addr, v)
		m.compare(m.a, v)
	default: // ISC
		v := m.read(addr) + 1
		m.write(addr, v)
		m.sbc(v)
	}
	return cyc
}
