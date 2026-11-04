package gameserver

import (
	"hash/fnv"
	"time"
)

// fishRollContext 只传递本次判鱼的区域和钩上饵，不改动存档格式。
type fishRollContext struct {
	x, z   float64
	hasPos bool
	spot   uint64
	now    time.Time
	baits  []string
	forRig bool
}

func rollContext(contexts []fishRollContext) fishRollContext {
	var c fishRollContext
	if len(contexts) > 0 {
		c = contexts[0]
	}
	if c.now.IsZero() {
		c.now = time.Now()
	}
	return c
}

func fishVariation(key string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return float64(h.Sum64()%10000) / 10000
}
