package util

import (
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"time"
	"unicode"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/common/hexutil"
	"github.com/dominant-strategies/go-quai/common/math"
)

var Ether = math.BigPow(10, 18)
var Shannon = math.BigPow(10, 9)

var pow256 = math.BigPow(2, 256)
var addressPattern = regexp.MustCompile("^0x[0-9a-fA-F]{40}$")
var zeroHash = regexp.MustCompile("^0?x?0+$")

func IsValidHexAddress(s string) bool {
	if IsZeroHash(s) || !addressPattern.MatchString(s) {
		return false
	}
	return true
}

func IsZeroHash(s string) bool {
	return zeroHash.MatchString(s)
}

func MakeTimestamp() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func GetTargetHex(diff *hexutil.Big) string {
	difficulty := (*big.Int)(diff)
	diff1 := new(big.Int).Div(pow256, difficulty)
	return string(hexutil.Encode(diff1.Bytes()))
}

func TargetHexToDiff(targetHex string) *big.Int {
	targetBytes := common.FromHex(targetHex)
	return new(big.Int).Div(pow256, new(big.Int).SetBytes(targetBytes))
}

func ToHex(n int64) string {
	return "0x0" + strconv.FormatInt(n, 16)
}

func FormatReward(reward *big.Int) string {
	return reward.String()
}

func FormatRatReward(reward *big.Rat) string {
	wei := new(big.Rat).SetInt(Ether)
	reward = reward.Quo(reward, wei)
	return reward.FloatString(8)
}

func StringInSlice(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

func MustParseDuration(s string) time.Duration {
	value, err := time.ParseDuration(s)
	if err != nil {
		panic("util: Can't parse duration `" + s + "`: " + err.Error())
	}
	return value
}

func String2Big(num string) *big.Int {
	n := new(big.Int)
	n.SetString(num, 0)
	return n
}

// for NiceHash...
// fixme: rounding error causes invalid shares
func DiffToTarget(diff float64) (target *big.Int) {
	mantissa := 0x0000ffff / diff
	exp := 1
	tmp := mantissa
	for tmp >= 256.0 {
		tmp /= 256.0
		exp++
	}
	for i := 0; i < exp; i++ {
		mantissa *= 256.0
	}
	target = new(big.Int).Lsh(big.NewInt(int64(mantissa)), uint(26-exp)*8)
	return
}

func DiffFloatToDiffInt(diffFloat float64) (diffInt *big.Int) {
	target := DiffToTarget(diffFloat)
	return new(big.Int).Div(pow256, target)
}

func LocationFromName(name string) (common.Location, error) {
	var loc common.Location
	var region string
	var zone string

	if name == "prime" {
		return loc, nil
	}

	for i := 0; i < len(name); i++ {
		if unicode.IsDigit(rune(name[i])) {
			region = name[:i]
			zone = name[i:]
			break
		}
	}

	if region == "" {
		region = name // No zone specified
	}

	switch region {
	case "cyprus":
		loc = append(loc, 0)
	case "paxos":
		loc = append(loc, 1)
	case "hydra":
		loc = append(loc, 2)
	default:
		err := errors.New("unknown region")
		return nil, err
	}

	if zone != "" {
		zone, err := strconv.Atoi(zone)
		if err == nil {
			loc = append(loc, byte(zone-1)) // Adjust because zones are 1-indexed in names
		} else {
			err = errors.New("invalid zone number")
			return nil, err
		}
	} else {
		// There was no zone specified, so return the region
		return loc, nil
	}
	return loc, nil
}
