package fake

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// Space facts: each answer carries one, in turn, so a fact that changes
// proves a fresh answer came back. Each version gets its own set (odd
// versions the Parker Solar Probe and the Sun, even ones light and deep
// space), so a canary split shows at a glance. Checked against NASA's
// mission pages; keep new ones to that standard.
var facts = [2][]string{
	{
		"Parker Solar Probe launched on August 12, 2018, on a Delta IV Heavy from Cape Canaveral.",
		"Parker Solar Probe is the first NASA spacecraft named after a living person: astrophysicist Eugene Parker.",
		"Eugene Parker predicted the solar wind in 1958, decades before a spacecraft could fly through it.",
		"Eugene Parker watched the probe named after him launch, at the age of 91.",
		"On December 24, 2024, Parker Solar Probe passed 3.8 million miles (6.1 million km) from the Sun's surface.",
		"At its closest approach, Parker Solar Probe moves at about 430,000 mph (692,000 km/h), faster than any other human-made object.",
		"At Parker Solar Probe's top speed you could fly from Washington, D.C., to Tokyo in under a minute.",
		"Parker Solar Probe's heat shield is a carbon composite foam sandwich 4.5 inches (11.4 cm) thick.",
		"The Sun-facing side of Parker's heat shield reaches about 2,500°F (1,370°C) while the instruments behind it stay near 85°F (29°C).",
		"Parker's heat shield is painted with a white ceramic coating to reflect as much sunlight as possible.",
		"Parker Solar Probe uses seven flybys of Venus to shrink its orbit closer to the Sun.",
		"In April 2021, Parker Solar Probe became the first spacecraft to fly through the Sun's corona.",
		"Parker Solar Probe's solar arrays are cooled by water pumped through them.",
		"Close to the Sun, Parker Solar Probe keeps its heat shield pointed sunward on its own: signals from Earth take minutes to arrive.",
		"Parker Solar Probe carries a memory card with more than 1.1 million names sent in by the public.",
		"Parker's Solar Probe Cup peeks around the heat shield to sample the solar wind straight from the Sun.",
		"Parker Solar Probe found the solar wind full of switchbacks: sudden S-shaped kinks in its magnetic field.",
		"Parker's WISPR camera photographed the surface of Venus through its clouds, in visible light, from orbit.",
		"In its final orbits, Parker Solar Probe circles the Sun every 88 days.",
		"The Sun's corona is hundreds of times hotter than its visible surface; why is one of the questions Parker was sent to answer.",
		"The Sun's visible surface is about 10,000°F (5,500°C).",
		"Sunlight takes about 8 minutes and 20 seconds to reach Earth.",
		"The Sun holds about 99.8% of all the mass in the solar system.",
		"About 1.3 million Earths would fit inside the Sun.",
		"The solar wind streams away from the Sun at about a million miles per hour.",
		"The Sun is about 4.6 billion years old.",
		"The Sun spins faster at its equator, about once every 25 days, than near its poles, about every 35.",
		"The Sun's corona can be seen with the naked eye only during a total solar eclipse.",
		"The Sun is a star: a hot ball of mostly hydrogen and helium.",
		"Every second, the Sun turns about 4 million tons of its mass into energy.",
	},
	{
		"Light travels exactly 299,792,458 meters per second in a vacuum: the meter is defined by it.",
		"Moonlight takes about 1.3 seconds to reach Earth.",
		"A light-year is about 5.88 trillion miles (9.46 trillion km).",
		"Light takes about 100,000 years to cross the Milky Way.",
		"Sunlight takes about 5.5 hours, on average, to reach Pluto.",
		"Radio signals to Mars take between about 3 and 22 minutes each way, depending on where the planets are.",
		"In glass, light travels about a third slower than in a vacuum.",
		"Voyager 1 crossed into interstellar space in August 2012, the first spacecraft to do so.",
		"Voyager 2 reached interstellar space in November 2018.",
		"Voyager 1 is the most distant human-made object, more than 15 billion miles (24 billion km) away.",
		"Around November 2026, Voyager 1 becomes the first spacecraft a full light-day from Earth.",
		"Each Voyager carries a Golden Record of sounds and images from Earth.",
		"Pioneer 10 was the first spacecraft to cross the asteroid belt and fly past Jupiter, in 1973.",
		"New Horizons flew past Pluto on July 14, 2015.",
		"Proxima Centauri, the closest star to the Sun, is about 4.24 light-years away.",
		"The Milky Way is about 100,000 light-years across.",
		"The Andromeda Galaxy, about 2.5 million light-years away, is the most distant thing most people can see with the naked eye.",
		"The universe is about 13.8 billion years old.",
		"The cosmic microwave background is light from about 380,000 years after the Big Bang.",
		"The James Webb Space Telescope works about a million miles (1.5 million km) from Earth.",
		"Webb's main mirror is 6.5 meters across, made of 18 gold-coated beryllium segments.",
		"Webb's five-layer sunshield is about the size of a tennis court.",
		"The Hubble Space Telescope launched in April 1990 aboard Space Shuttle Discovery.",
		"The International Space Station travels at about 17,500 mph and circles Earth about every 90 minutes.",
		"A teaspoon of neutron star would weigh about a billion tons on Earth.",
		"The first image of a black hole, M87*, was released in April 2019 by the Event Horizon Telescope.",
		"Saturn is less dense than water.",
		"A day on Venus is longer than its year.",
		"Olympus Mons on Mars is about two and a half times as tall as Mount Everest.",
		"Jupiter's Great Red Spot is a storm wider than Earth.",
	},
}

// factSet picks a version's set: v1, v3, ... (and no version) get the
// Parker Probe and the Sun; v2, v4, ... light and deep space. A version
// with no number is hashed.
func factSet(version string) int {
	v := strings.TrimRight(version, " ")
	i := len(v)
	for i > 0 && v[i-1] >= '0' && v[i-1] <= '9' {
		i--
	}
	if n, err := strconv.Atoi(v[i:]); err == nil {
		return (n + 1) % 2
	}
	h := 0
	for _, c := range version {
		h += int(c)
	}
	return h % 2
}

// factTeller hands out a set's facts in turn.
type factTeller struct {
	set  []string
	next atomic.Uint64
}

func newFactTeller(version string, start uint64) *factTeller {
	t := &factTeller{set: facts[factSet(version)]}
	t.next.Store(start)
	return t
}

func (t *factTeller) fact() string {
	return t.set[(t.next.Add(1)-1)%uint64(len(t.set))]
}
