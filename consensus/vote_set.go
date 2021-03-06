package consensus

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	"github.com/tendermint/tendermint/account"
	"github.com/tendermint/tendermint/binary"
	. "github.com/tendermint/tendermint/common"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

// VoteSet helps collect signatures from validators at each height+round
// for a predefined vote type.
// Note that there three kinds of votes: prevotes, precommits, and commits.
// A commit of prior rounds can be added added in lieu of votes/precommits.
// NOTE: Assumes that the sum total of voting power does not exceed MaxUInt64.
type VoteSet struct {
	height int
	round  int
	type_  byte

	mtx           sync.Mutex
	valSet        *sm.ValidatorSet
	votes         []*types.Vote    // validator index -> vote
	votesBitArray *BitArray        // validator index -> has vote?
	votesByBlock  map[string]int64 // string(blockHash)+string(blockParts) -> vote sum.
	totalVotes    int64
	maj23Hash     []byte
	maj23Parts    types.PartSetHeader
	maj23Exists   bool
}

// Constructs a new VoteSet struct used to accumulate votes for given height/round.
func NewVoteSet(height int, round int, type_ byte, valSet *sm.ValidatorSet) *VoteSet {
	if height == 0 {
		panic("Cannot make VoteSet for height == 0, doesn't make sense.")
	}
	return &VoteSet{
		height:        height,
		round:         round,
		type_:         type_,
		valSet:        valSet,
		votes:         make([]*types.Vote, valSet.Size()),
		votesBitArray: NewBitArray(valSet.Size()),
		votesByBlock:  make(map[string]int64),
		totalVotes:    0,
	}
}

func (voteSet *VoteSet) Height() int {
	if voteSet == nil {
		return 0
	} else {
		return voteSet.height
	}
}

func (voteSet *VoteSet) Round() int {
	if voteSet == nil {
		return 0
	} else {
		return voteSet.round
	}
}

func (voteSet *VoteSet) Size() int {
	if voteSet == nil {
		return 0
	} else {
		return voteSet.valSet.Size()
	}
}

// Returns added=true, index if vote was added
// Otherwise returns err=ErrVote[UnexpectedStep|InvalidAccount|InvalidSignature|InvalidBlockHash|ConflictingSignature]
// Duplicate votes return added=false, err=nil.
// NOTE: vote should not be mutated after adding.
func (voteSet *VoteSet) AddByIndex(valIndex int, vote *types.Vote) (added bool, index int, err error) {
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()

	return voteSet.addByIndex(valIndex, vote)
}

// Returns added=true, index if vote was added
// Otherwise returns err=ErrVote[UnexpectedStep|InvalidAccount|InvalidSignature|InvalidBlockHash|ConflictingSignature]
// Duplicate votes return added=false, err=nil.
// NOTE: vote should not be mutated after adding.
func (voteSet *VoteSet) AddByAddress(address []byte, vote *types.Vote) (added bool, index int, err error) {
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()

	// Ensure that signer is a validator.
	valIndex, val := voteSet.valSet.GetByAddress(address)
	if val == nil {
		return false, 0, types.ErrVoteInvalidAccount
	}

	return voteSet.addVote(val, valIndex, vote)
}

func (voteSet *VoteSet) addByIndex(valIndex int, vote *types.Vote) (bool, int, error) {
	// Ensure that signer is a validator.
	_, val := voteSet.valSet.GetByIndex(valIndex)
	if val == nil {
		return false, 0, types.ErrVoteInvalidAccount
	}

	return voteSet.addVote(val, valIndex, vote)
}

func (voteSet *VoteSet) addVote(val *sm.Validator, valIndex int, vote *types.Vote) (bool, int, error) {

	// Make sure the step matches. (or that vote is commit && round < voteSet.round)
	if (vote.Height != voteSet.height) ||
		(vote.Round != voteSet.round) ||
		(vote.Type != voteSet.type_) {
		return false, 0, types.ErrVoteUnexpectedStep
	}

	// Check signature.
	if !val.PubKey.VerifyBytes(account.SignBytes(config.GetString("chain_id"), vote), vote.Signature) {
		// Bad signature.
		return false, 0, types.ErrVoteInvalidSignature
	}

	// If vote already exists, return false.
	if existingVote := voteSet.votes[valIndex]; existingVote != nil {
		if bytes.Equal(existingVote.BlockHash, vote.BlockHash) {
			return false, valIndex, nil
		} else {
			return false, valIndex, &types.ErrVoteConflictingSignature{
				VoteA: existingVote,
				VoteB: vote,
			}
		}
	}

	// Add vote.
	voteSet.votes[valIndex] = vote
	voteSet.votesBitArray.SetIndex(valIndex, true)
	blockKey := string(vote.BlockHash) + string(binary.BinaryBytes(vote.BlockParts))
	totalBlockHashVotes := voteSet.votesByBlock[blockKey] + val.VotingPower
	voteSet.votesByBlock[blockKey] = totalBlockHashVotes
	voteSet.totalVotes += val.VotingPower

	// If we just nudged it up to two thirds majority, add it.
	if totalBlockHashVotes > voteSet.valSet.TotalVotingPower()*2/3 &&
		(totalBlockHashVotes-val.VotingPower) <= voteSet.valSet.TotalVotingPower()*2/3 {
		voteSet.maj23Hash = vote.BlockHash
		voteSet.maj23Parts = vote.BlockParts
		voteSet.maj23Exists = true
	}

	return true, valIndex, nil
}

func (voteSet *VoteSet) BitArray() *BitArray {
	if voteSet == nil {
		return nil
	}
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()
	return voteSet.votesBitArray.Copy()
}

func (voteSet *VoteSet) GetByIndex(valIndex int) *types.Vote {
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()
	return voteSet.votes[valIndex]
}

func (voteSet *VoteSet) GetByAddress(address []byte) *types.Vote {
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()
	valIndex, val := voteSet.valSet.GetByAddress(address)
	if val == nil {
		panic("GetByAddress(address) returned nil")
	}
	return voteSet.votes[valIndex]
}

func (voteSet *VoteSet) HasTwoThirdsMajority() bool {
	if voteSet == nil {
		return false
	}
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()
	return voteSet.maj23Exists
}

func (voteSet *VoteSet) HasTwoThirdsAny() bool {
	if voteSet == nil {
		return false
	}
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()
	return voteSet.totalVotes > voteSet.valSet.TotalVotingPower()*2/3
}

// Returns either a blockhash (or nil) that received +2/3 majority.
// If there exists no such majority, returns (nil, false).
func (voteSet *VoteSet) TwoThirdsMajority() (hash []byte, parts types.PartSetHeader, ok bool) {
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()
	if voteSet.maj23Exists {
		return voteSet.maj23Hash, voteSet.maj23Parts, true
	} else {
		return nil, types.PartSetHeader{}, false
	}
}

func (voteSet *VoteSet) String() string {
	if voteSet == nil {
		return "nil-VoteSet"
	}
	return voteSet.StringIndented("")
}

func (voteSet *VoteSet) StringIndented(indent string) string {
	voteStrings := make([]string, len(voteSet.votes))
	for i, vote := range voteSet.votes {
		if vote == nil {
			voteStrings[i] = "nil-Vote"
		} else {
			voteStrings[i] = vote.String()
		}
	}
	return fmt.Sprintf(`VoteSet{
%s  H:%v R:%v T:%v
%s  %v
%s  %v
%s}`,
		indent, voteSet.height, voteSet.round, voteSet.type_,
		indent, strings.Join(voteStrings, "\n"+indent+"  "),
		indent, voteSet.votesBitArray,
		indent)
}

func (voteSet *VoteSet) StringShort() string {
	if voteSet == nil {
		return "nil-VoteSet"
	}
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()
	return fmt.Sprintf(`VoteSet{H:%v R:%v T:%v +2/3:%v %v}`,
		voteSet.height, voteSet.round, voteSet.type_, voteSet.maj23Exists, voteSet.votesBitArray)
}

//--------------------------------------------------------------------------------
// Validation

func (voteSet *VoteSet) MakeValidation() *types.Validation {
	if voteSet.type_ != types.VoteTypePrecommit {
		panic("Cannot MakeValidation() unless VoteSet.Type is types.VoteTypePrecommit")
	}
	voteSet.mtx.Lock()
	defer voteSet.mtx.Unlock()
	if len(voteSet.maj23Hash) == 0 {
		panic("Cannot MakeValidation() unless a blockhash has +2/3")
	}
	precommits := make([]*types.Vote, voteSet.valSet.Size())
	voteSet.valSet.Iterate(func(valIndex int, val *sm.Validator) bool {
		vote := voteSet.votes[valIndex]
		if vote == nil {
			return false
		}
		if !bytes.Equal(vote.BlockHash, voteSet.maj23Hash) {
			return false
		}
		if !vote.BlockParts.Equals(voteSet.maj23Parts) {
			return false
		}
		precommits[valIndex] = vote
		return false
	})
	return &types.Validation{
		Precommits: precommits,
	}
}
