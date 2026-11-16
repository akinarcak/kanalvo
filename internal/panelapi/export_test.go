package panelapi

import "kanalvo/internal/passhash"

// SetHashGate, testlerin şifre işlemi kapısını (ör. hep dolu bir kapı) değiştirmesi içindir.
func (h *Handler) SetHashGate(g *passhash.Gate) { h.hashes = g }
