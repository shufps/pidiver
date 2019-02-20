package api

// complaints or suggestions pls to pmaxuw on discord

import (
	"errors"
	"net/http"
	"sync"
	"time"

	//    "github.com/spf13/viper"

	"github.com/gin-gonic/gin"
	"github.com/iotaledger/iota.go/consts"
	"github.com/iotaledger/iota.go/curl"
	"github.com/iotaledger/iota.go/pow"
	"github.com/iotaledger/iota.go/trinary"
	"github.com/shufps/pidiver/server/config"
	"github.com/shufps/pidiver/server/logs"
)

const (
	// not defined in giota library
	MaxTimestampValue = 3812798742493 //int64(3^27 - 1) / 2

)

const (
	TryteAlphabet             = "9ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	MinTryteValue             = -13
	MaxTryteValue             = 13
	SignatureSize             = 6561
	HashSize                  = 243
	Depth                     = 3
	Radix                     = 3
	DefaultMinWeightMagnitude = 14
)

var (
	powLock                 = &sync.Mutex{}
	maxMinWeightMagnitude   = 0
	maxTransactions         = 0
	useDiverDriver          = false
	interruptAttachToTangle = false
	powInitialized          = false
	powType                 string
	powVersion              string
	serverVersion           string
)

// Int2Trits converts int64 to trits.
func Int2Trits(v int64, size int) trinary.Trits {
	tr := make(trinary.Trits, size)
	neg := false
	if v < 0 {
		v = -v
		neg = true
	}

	for i := 0; v != 0 && i < size; i++ {
		tr[i] = int8((v+1)%Radix) - 1

		if neg {
			tr[i] = -tr[i]
		}

		v = (v + 1) / Radix
	}
	return tr
}

func Int2Runes(v int64, size int) []rune {
	trytes, _ := trinary.TritsToTrytes(Int2Trits(v, size))
	return toRunes(trytes)
}

func init() {
	addAPICall("attachToTangle", attachToTangle, mainAPICalls)
	addAPICall("interruptAttachingToTangle", interruptAttachingToTangle, mainAPICalls)
}

func startAttach() {
	maxMinWeightMagnitude = config.AppConfig.GetInt("api.pow.maxMinWeightMagnitude")
	maxTransactions = config.AppConfig.GetInt("api.pow.maxTransactions")

	logs.Log.Debug("maxMinWeightMagnitude:", maxMinWeightMagnitude)
	logs.Log.Debug("maxTransactions:", maxTransactions)
	logs.Log.Debug("useDiverDriver:", useDiverDriver)

}

func IsValidPoW(hash trinary.Trits, mwm int) bool {
	for i := len(hash) - mwm; i < len(hash); i++ {
		if hash[i] != 0 {
			return false
		}
	}
	return true
}

func toRunesCheckTrytes(s string, length int) ([]rune, error) {
	if len(s) != length {
		return []rune{}, errors.New("invalid length")
	}
	if _, err := trinary.NewTrytes(s); err != nil {
		return []rune{}, err
	}
	return []rune(string(s)), nil
}

func toRunes(t trinary.Trytes) []rune {
	return []rune(string(t))
}

// interrupts not PoW itselfe (no PoW of giota support interrupts) but stops
// attatchToTangle after the last transaction PoWed
func interruptAttachingToTangle(request Request, c *gin.Context, t time.Time) {
	interruptAttachToTangle = true
	c.JSON(http.StatusOK, gin.H{})
}

func getTimestamp() int64 {
	return time.Now().UnixNano() / (int64(time.Millisecond) / int64(time.Nanosecond)) // time.Nanosecond should always be 1 ... but if not ...^^
}

// attachToTangle
// do everything with trytes and save time by not convertig to trits and back
// all constants have to be divided by 3
func attachToTangle(request Request, c *gin.Context, t time.Time) {
	// only one attatchToTangle allowed in parallel
	powLock.Lock()
	defer powLock.Unlock()

	interruptAttachToTangle = false

	var returnTrytes []string

	trunkTransaction, err := toRunesCheckTrytes(request.TrunkTransaction, consts.TrunkTransactionTrinarySize/3)
	if err != nil {
		replyError("Invalid trunkTransaction-Trytes", c)
		return
	}

	branchTransaction, err := toRunesCheckTrytes(request.BranchTransaction, consts.BranchTransactionTrinarySize/3)
	if err != nil {
		replyError("Invalid branchTransaction-Trytes", c)
		return
	}

	minWeightMagnitude := request.MinWeightMagnitude

	// restrict minWeightMagnitude
	if minWeightMagnitude > maxMinWeightMagnitude {
		replyError("MinWeightMagnitude too high", c)
		return
	}

	trytes := request.Trytes

	// limit number of transactions in a bundle
	if len(trytes) > maxTransactions {
		replyError("Too many transactions", c)
		return
	}
	returnTrytes = make([]string, len(trytes))
	inputRunes := make([][]rune, len(trytes))

	// validate input trytes before doing PoW
	for idx, tryte := range trytes {
		if runes, err := toRunesCheckTrytes(tryte, consts.TransactionTrinarySize/3); err != nil {
			replyError("Error in Tryte input", c)
			return
		} else {
			inputRunes[idx] = runes
		}
	}

	var prevTransaction []rune

	for idx, runes := range inputRunes {
		if interruptAttachToTangle {
			replyError("attatchToTangle interrupted", c)
			return
		}
		timestamp := getTimestamp()
		//branch and trunk
		tmp := prevTransaction
		if len(prevTransaction) == 0 {
			tmp = trunkTransaction
		}
		copy(runes[consts.TrunkTransactionTrinaryOffset/3:], tmp[:consts.TrunkTransactionTrinarySize/3])

		tmp = trunkTransaction
		if len(prevTransaction) == 0 {
			tmp = branchTransaction
		}
		copy(runes[consts.BranchTransactionTrinaryOffset/3:], tmp[:consts.BranchTransactionTrinarySize/3])

		//attachment fields: tag and timestamps
		//tag - copy the obsolete tag to the attachment tag field only if tag isn't set.
		if string(runes[consts.TagTrinaryOffset/3:(consts.TagTrinaryOffset+consts.TagTrinarySize)/3]) == "999999999999999999999999999" {
			copy(runes[consts.TagTrinaryOffset/3:], runes[consts.ObsoleteTagTrinaryOffset/3:(consts.ObsoleteTagTrinaryOffset+consts.ObsoleteTagTrinarySize)/3])
		}

		runesTimeStamp := Int2Runes(timestamp, consts.AttachmentTimestampTrinarySize)

		runesTimeStampLowerBoundary := Int2Runes(0, consts.AttachmentTimestampLowerBoundTrinarySize)
		runesTimeStampUpperBoundary := Int2Runes(MaxTimestampValue, consts.AttachmentTimestampUpperBoundTrinarySize)

		copy(runes[consts.AttachmentTimestampTrinaryOffset/3:], runesTimeStamp[:consts.AttachmentTimestampTrinarySize/3])
		copy(runes[consts.AttachmentTimestampLowerBoundTrinaryOffset/3:], runesTimeStampLowerBoundary[:consts.AttachmentTimestampLowerBoundTrinarySize/3])
		copy(runes[consts.AttachmentTimestampUpperBoundTrinaryOffset/3:], runesTimeStampUpperBoundary[:consts.AttachmentTimestampUpperBoundTrinarySize/3])

		var powFunc pow.ProofOfWorkFunc

		// do pow
		logs.Log.Info("[PoW] Using PiDiver")
		powFunc = powFuncs[0]

		startTime := time.Now()
		nonceTrytes, err := powFunc(trinary.Trytes(runes), minWeightMagnitude)
		if err != nil || len(nonceTrytes) != consts.NonceTrinarySize/3 {
			replyError("PoW failed!", c)
			return
		}
		elapsedTime := time.Now().Sub(startTime)
		logs.Log.Info("[PoW] Needed", elapsedTime)

		// copy nonce to runes
		copy(runes[consts.NonceTrinaryOffset/3:], toRunes(nonceTrytes)[:consts.NonceTrinarySize/3])

		logs.Log.Debug(string(runes))
		verifyTrytes, err := trinary.NewTrytes(string(runes))
		if err != nil {
			replyError("Trytes got corrupted", c)
			return
		}

		//validate PoW - throws exception if invalid
		hash, _ := curl.HashTrytes(verifyTrytes)
		hashTrits, _ := trinary.TrytesToTrits(hash)
		if !IsValidPoW(hashTrits, minWeightMagnitude) {
			replyError("Nonce verify failed", c)
			return
		}

		logs.Log.Info("[PoW] Verified!")

		returnTrytes[idx] = string(runes)

		prevTransaction = toRunes(hash)
	}

	c.JSON(http.StatusOK, gin.H{
		"trytes": returnTrytes,
	})
}
