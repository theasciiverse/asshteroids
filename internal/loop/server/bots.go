package server

import (
	"log"
	"math"
	"math/rand"

	"github.com/tomz197/asteroids/internal/loop/config"
	"github.com/tomz197/asteroids/internal/object"
)

// BotNames are the computer pilots' call signs, after asteroids.
var BotNames = []string{"Vesta", "Pallas", "Juno", "Hygiea", "Eros", "Ida", "Gaspra", "Psyche"}

// rocksPerBot is how much each computer pilot adds to the field's rocks
// (counted as the spawner does: a large rock is 4).
const rocksPerBot = 60

// botBrain flies a ship the way a player would: it only ever sets the same
// keys a person holds (rotate, thrust, fire), so it plays by the same rules.
type botBrain struct {
	lives     int
	think     float64 // seconds until the next look around
	aimNoise  float64 // this pilot's aiming error, in radians
	hunter    float64 // chance of picking a nearby ship over a rock
	target    any     // *object.Asteroid or *object.User
	waypointX float64
	waypointY float64
	roam      float64 // seconds until a new waypoint
	input     object.Input
}

func newBotBrain() *botBrain {
	return &botBrain{
		lives:    config.InitialLives,
		aimNoise: 0.02 + rand.Float64()*0.06,
		hunter:   0.10 + rand.Float64()*0.15,
	}
}

// AddBot puts a computer pilot in the field and returns its client ID.
func (s *Server) AddBot(name string) int {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextClientID
	s.nextClientID++
	handle := &ClientHandle{
		ID:       id,
		Username: name,
		EventsCh: make(chan ClientEvent, 16),
		brain:    newBotBrain(),
		isBot:    true,
	}
	s.clients[id] = handle
	s.spawnLocked(handle)
	// a pilot shoots rocks too, so the field keeps more of them
	s.spawner.AddTarget(rocksPerBot)
	log.Printf("bot joined id=%d name=%s", id, name)
	return id
}

// SetAutopilot lets the computer fly a person's ship (the demo).
func (s *Server) SetAutopilot(clientID int, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	handle, ok := s.clients[clientID]
	if !ok {
		if s.pendingAutopilot == nil {
			s.pendingAutopilot = make(map[int]bool)
		}
		s.pendingAutopilot[clientID] = on
		return
	}
	{
		if on {
			handle.brain = newBotBrain()
		} else {
			handle.brain = nil
		}
	}
}

// spawnLocked puts a new ship for handle somewhere in the world.
// Must be called with s.mu held.
func (s *Server) spawnLocked(handle *ClientHandle) {
	if handle.Player != nil {
		s.removeObjectLocked(handle.Player)
	}
	x := rand.Float64() * float64(config.WorldWidth)
	y := rand.Float64() * float64(config.WorldHeight)
	player := object.NewUser(x, y)
	player.OwnerID = handle.ID
	player.Username = handle.Username
	handle.Player = player
	handle.InvincibleTime = config.InvincibilityTime.Seconds()
	s.world.AddObject(player)
}

// updateBots respawns computer pilots and works out what every
// computer-flown ship does this tick. Must be called with s.mu held.
func (s *Server) updateBots(dt float64) {
	for _, handle := range s.clients {
		b := handle.brain
		if b == nil {
			continue
		}
		if handle.Player == nil {
			// people on autopilot respawn through their own client
			if handle.isBot && handle.RespawnTimeRemaining <= 0 {
				if b.lives <= 0 {
					b.lives = config.InitialLives
					handle.Score = 0
				}
				s.spawnLocked(handle)
				log.Printf("bot respawned id=%d name=%s lives=%d", handle.ID, handle.Username, b.lives)
			}
			continue
		}
		handle.Input = s.fly(handle, b, dt)
	}
}

// wrapDelta is the shortest way from a to b on a wrapping axis.
func wrapDelta(a, b, size float64) float64 {
	return math.Remainder(b-a, size)
}

// fly is one tick of a computer pilot.
func (s *Server) fly(handle *ClientHandle, b *botBrain, dt float64) object.Input {
	u := handle.Player
	ww, wh := float64(config.WorldWidth), float64(config.WorldHeight)

	b.think -= dt
	b.roam -= dt
	if b.think <= 0 {
		b.think = 0.15 + rand.Float64()*0.2
		b.target = s.pickTarget(handle, b)
	}
	if b.roam <= 0 {
		b.roam = 6 + rand.Float64()*6
		b.waypointX, b.waypointY = s.pickWaypoint(handle)
	}

	in := object.Input{Number: -1}

	// whatever it chose may be gone by now
	var tx, ty, tvx, tvy, tr float64
	ok := false
	switch t := b.target.(type) {
	case *object.Asteroid:
		if !t.IsDestroyed() {
			tx, ty, tvx, tvy, tr, ok = t.X, t.Y, t.VX, t.VY, t.Radius, true
		}
	case *object.User:
		if s.alive(t) {
			tx, ty, tvx, tvy, tr, ok = t.X, t.Y, t.VX, t.VY, t.GetRadius(), true
		}
	}

	var aim float64
	if ok {
		dx := wrapDelta(u.X, tx, ww)
		dy := wrapDelta(u.Y, ty, wh)
		dist := math.Hypot(dx, dy)
		// lead the target: shots carry the ship's own speed, so only the
		// difference in velocity counts
		t := dist / object.ProjectileSpeed
		rvx, rvy := tvx-u.VX, tvy-u.VY
		aim = math.Atan2(dy+rvy*t, dx+rvx*t) + b.aimNoise*math.Sin(float64(handle.ID)+dist)
		diff := math.Remainder(aim-u.Angle, 2*math.Pi)
		steer(&in, diff)
		// fire when lined up and in range; a big rock forgives more
		tolerance := 0.06 + math.Atan2(tr, dist+1)
		if math.Abs(diff) < tolerance && dist < 70 {
			in.Space = true
		}
		// too close to a rock: back off by turning away and burning
		if _, rock := b.target.(*object.Asteroid); rock && dist-tr < 9 {
			away := math.Remainder(math.Atan2(-dy, -dx)-u.Angle, 2*math.Pi)
			if math.Abs(away) < 0.8 {
				in.Up = true
			}
		}
		// chase a ship that is out of range
		if _, ship := b.target.(*object.User); ship && dist > 45 && math.Abs(diff) < 0.5 {
			in.Up = true
		}
		return in
	}

	// nothing worth shooting nearby: head for the waypoint
	dx := wrapDelta(u.X, b.waypointX, ww)
	dy := wrapDelta(u.Y, b.waypointY, wh)
	diff := math.Remainder(math.Atan2(dy, dx)-u.Angle, 2*math.Pi)
	steer(&in, diff)
	if math.Abs(diff) < 0.4 && math.Hypot(dx, dy) > 15 {
		in.Up = true
	}
	return in
}

// steer turns towards an angle difference, with a little dead zone.
func steer(in *object.Input, diff float64) {
	switch {
	case diff > 0.03:
		in.Right = true
	case diff < -0.03:
		in.Left = true
	}
}

// alive reports whether a ship is still flying.
func (s *Server) alive(u *object.User) bool {
	for _, h := range s.clients {
		if h.Player == u {
			return true
		}
	}
	return false
}

// pickTarget chooses what to shoot: a rock about to hit, a nearby ship
// now and then, or the nearest rock.
func (s *Server) pickTarget(handle *ClientHandle, b *botBrain) any {
	u := handle.Player
	ww, wh := float64(config.WorldWidth), float64(config.WorldHeight)

	var best *object.Asteroid
	bestScore := math.MaxFloat64
	for _, obj := range s.world.Objects {
		a, ok := obj.(*object.Asteroid)
		if !ok || a.IsDestroyed() || a.IsProtected() {
			continue
		}
		dx := wrapDelta(u.X, a.X, ww)
		dy := wrapDelta(u.Y, a.Y, wh)
		dist := math.Hypot(dx, dy) - a.Radius
		if dist > 60 {
			continue
		}
		// rocks coming this way matter more
		closing := -(dx*(a.VX-u.VX) + dy*(a.VY-u.VY)) / (dist + a.Radius + 0.1)
		score := dist - closing*0.8
		if score < bestScore {
			best, bestScore = a, score
		}
	}

	// a rock right on top of us comes first
	if best != nil && bestScore < 12 {
		return best
	}

	if rand.Float64() < b.hunter {
		var prey *object.User
		preyDist := 40.0
		for _, h := range s.clients {
			if h == handle || h.Player == nil || h.InvincibleTime > 0 {
				continue
			}
			dx := wrapDelta(u.X, h.Player.X, ww)
			dy := wrapDelta(u.Y, h.Player.Y, wh)
			if d := math.Hypot(dx, dy); d < preyDist {
				prey, preyDist = h.Player, d
			}
		}
		if prey != nil {
			return prey
		}
	}
	if best != nil {
		return best
	}
	return nil
}

// pickWaypoint heads somewhere near another ship, so pilots find company,
// or anywhere at all when there is none.
func (s *Server) pickWaypoint(handle *ClientHandle) (float64, float64) {
	var ships []*object.User
	for _, h := range s.clients {
		if h != handle && h.Player != nil {
			ships = append(ships, h.Player)
		}
	}
	if len(ships) > 0 {
		t := ships[rand.Intn(len(ships))]
		return t.X + (rand.Float64()-0.5)*80, t.Y + (rand.Float64()-0.5)*80
	}
	return rand.Float64() * float64(config.WorldWidth), rand.Float64() * float64(config.WorldHeight)
}
