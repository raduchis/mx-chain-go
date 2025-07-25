package trie

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/multiversx/mx-chain-core-go/core"
	"github.com/multiversx/mx-chain-core-go/core/check"
	"github.com/multiversx/mx-chain-core-go/hashing"
	"github.com/multiversx/mx-chain-core-go/marshal"
	logger "github.com/multiversx/mx-chain-logger-go"
	vmcommon "github.com/multiversx/mx-chain-vm-common-go"

	"github.com/multiversx/mx-chain-go/common"
	"github.com/multiversx/mx-chain-go/dataRetriever"
	"github.com/multiversx/mx-chain-go/errors"
	"github.com/multiversx/mx-chain-go/trie/keyBuilder"
	"github.com/multiversx/mx-chain-go/trie/statistics"
)

var log = logger.GetOrCreate("trie")

var _ dataRetriever.TrieDataGetter = (*patriciaMerkleTrie)(nil)

const (
	extension = iota
	leaf
	branch
)

const rootDepthLevel = 0

type patriciaMerkleTrie struct {
	root node

	trieStorage             common.StorageManager
	marshalizer             marshal.Marshalizer
	hasher                  hashing.Hasher
	enableEpochsHandler     common.EnableEpochsHandler
	trieNodeVersionVerifier core.TrieNodeVersionVerifier
	mutOperation            sync.RWMutex

	oldHashes            [][]byte
	oldRoot              []byte
	maxTrieLevelInMemory uint
	chanClose            chan struct{}
}

// NewTrie creates a new Patricia Merkle Trie
func NewTrie(
	trieStorage common.StorageManager,
	msh marshal.Marshalizer,
	hsh hashing.Hasher,
	enableEpochsHandler common.EnableEpochsHandler,
	maxTrieLevelInMemory uint,
) (*patriciaMerkleTrie, error) {
	if check.IfNil(trieStorage) {
		return nil, ErrNilTrieStorage
	}
	if check.IfNil(msh) {
		return nil, ErrNilMarshalizer
	}
	if check.IfNil(hsh) {
		return nil, ErrNilHasher
	}
	if check.IfNil(enableEpochsHandler) {
		return nil, errors.ErrNilEnableEpochsHandler
	}
	if maxTrieLevelInMemory == 0 {
		return nil, ErrInvalidLevelValue
	}
	log.Trace("created new trie", "max trie level in memory", maxTrieLevelInMemory)

	tnvv, err := core.NewTrieNodeVersionVerifier(enableEpochsHandler)
	if err != nil {
		return nil, err
	}

	return &patriciaMerkleTrie{
		trieStorage:             trieStorage,
		marshalizer:             msh,
		hasher:                  hsh,
		oldHashes:               make([][]byte, 0),
		oldRoot:                 make([]byte, 0),
		maxTrieLevelInMemory:    maxTrieLevelInMemory,
		chanClose:               make(chan struct{}),
		enableEpochsHandler:     enableEpochsHandler,
		trieNodeVersionVerifier: tnvv,
	}, nil
}

// Get starts at the root and searches for the given key.
// If the key is present in the tree, it returns the corresponding value
func (tr *patriciaMerkleTrie) Get(key []byte) ([]byte, uint32, error) {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	if tr.root == nil {
		return nil, 0, nil
	}
	hexKey := keyBytesToHex(key)

	val, depth, err := tr.root.tryGet(hexKey, rootDepthLevel, tr.trieStorage)
	if err != nil {
		err = fmt.Errorf("trie get error: %w, for key %v", err, hex.EncodeToString(key))
		return nil, depth, err
	}

	return val, depth, nil
}

// Update updates the value at the given key.
// If the key is not in the trie, it will be added.
// If the value is empty, the key will be removed from the trie
func (tr *patriciaMerkleTrie) Update(key, value []byte) error {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	log.Trace("update trie", "key", key, "val", value)

	return tr.update(key, value, core.NotSpecified)
}

// UpdateWithVersion does the same thing as Update, but the new leaf that is created will be of the specified version
func (tr *patriciaMerkleTrie) UpdateWithVersion(key []byte, value []byte, version core.TrieNodeVersion) error {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	log.Trace("update trie with version", "key", key, "val", value, "version", version)

	return tr.update(key, value, version)
}

func (tr *patriciaMerkleTrie) update(key []byte, value []byte, version core.TrieNodeVersion) error {
	hexKey := keyBytesToHex(key)
	if len(value) != 0 {
		newData := core.TrieData{
			Key:     hexKey,
			Value:   value,
			Version: version,
		}

		if tr.root == nil {
			newRoot, err := newLeafNode(newData, tr.marshalizer, tr.hasher)
			if err != nil {
				return err
			}

			tr.root = newRoot
			return nil
		}

		if !tr.root.isDirty() {
			tr.oldRoot = tr.root.getHash()
		}

		newRoot, oldHashes, err := tr.root.insert(newData, tr.trieStorage)
		if err != nil {
			return err
		}

		if check.IfNil(newRoot) {
			return nil
		}

		tr.root = newRoot
		tr.oldHashes = append(tr.oldHashes, oldHashes...)

		logArrayWithTrace("oldHashes after insert", "hash", oldHashes)
	} else {
		return tr.delete(hexKey)
	}

	return nil
}

// Delete removes the node that has the given key from the tree
func (tr *patriciaMerkleTrie) Delete(key []byte) error {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	hexKey := keyBytesToHex(key)
	return tr.delete(hexKey)
}

func (tr *patriciaMerkleTrie) delete(hexKey []byte) error {
	if tr.root == nil {
		return nil
	}

	if !tr.root.isDirty() {
		tr.oldRoot = tr.root.getHash()
	}

	_, newRoot, oldHashes, err := tr.root.delete(hexKey, tr.trieStorage)
	if err != nil {
		return err
	}
	tr.root = newRoot
	tr.oldHashes = append(tr.oldHashes, oldHashes...)
	logArrayWithTrace("oldHashes after delete", "hash", oldHashes)

	return nil
}

// RootHash returns the hash of the root node
func (tr *patriciaMerkleTrie) RootHash() ([]byte, error) {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	return tr.getRootHash()
}

func (tr *patriciaMerkleTrie) getRootHash() ([]byte, error) {
	if tr.root == nil {
		return common.EmptyTrieHash, nil
	}

	hash := tr.root.getHash()
	if hash != nil {
		return hash, nil
	}
	err := tr.root.setRootHash()
	if err != nil {
		return nil, err
	}
	return tr.root.getHash(), nil
}

// Commit adds all the dirty nodes to the database
func (tr *patriciaMerkleTrie) Commit() error {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	if tr.root == nil {
		log.Trace("trying to commit empty trie")
		return nil
	}
	if !tr.root.isDirty() {
		log.Trace("trying to commit clean trie", "root", tr.root.getHash())
		return nil
	}
	err := tr.root.setRootHash()
	if err != nil {
		return err
	}

	tr.oldRoot = make([]byte, 0)
	tr.oldHashes = make([][]byte, 0)

	if log.GetLevel() == logger.LogTrace {
		log.Trace("started committing trie", "trie", tr.root.getHash())
	}

	err = tr.root.commitDirty(0, tr.maxTrieLevelInMemory, tr.trieStorage, tr.trieStorage)
	if err != nil {
		return err
	}

	return nil
}

// Recreate returns a new trie, given the options
func (tr *patriciaMerkleTrie) Recreate(options common.RootHashHolder) (common.Trie, error) {
	if check.IfNil(options) {
		return nil, ErrNilRootHashHolder
	}

	if !options.GetEpoch().HasValue {
		return tr.recreate(options.GetRootHash(), tr.trieStorage)
	}

	tsmie, err := newTrieStorageManagerInEpoch(tr.trieStorage, options.GetEpoch().Value)
	if err != nil {
		return nil, err
	}

	return tr.recreate(options.GetRootHash(), tsmie)
}

func (tr *patriciaMerkleTrie) recreate(root []byte, tsm common.StorageManager) (*patriciaMerkleTrie, error) {
	if common.IsEmptyTrie(root) {
		return NewTrie(
			tr.trieStorage,
			tr.marshalizer,
			tr.hasher,
			tr.enableEpochsHandler,
			tr.maxTrieLevelInMemory,
		)
	}

	newTr, _, err := tr.recreateFromDb(root, tsm)
	if err != nil {
		if core.IsClosingError(err) {
			log.Debug("could not recreate", "rootHash", root, "error", err)
			return nil, err
		}

		log.Warn("trie recreate error:", "error", err, "root", hex.EncodeToString(root))
		return nil, err
	}

	return newTr, nil
}

// String outputs a graphical view of the trie. Mainly used in tests/debugging
func (tr *patriciaMerkleTrie) String() string {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	writer := bytes.NewBuffer(make([]byte, 0))

	if tr.root == nil {
		_, _ = fmt.Fprintln(writer, "*** EMPTY TRIE ***")
	} else {
		tr.root.print(writer, 0, tr.trieStorage)
	}

	return writer.String()
}

// IsInterfaceNil returns true if there is no value under the interface
func (tr *patriciaMerkleTrie) IsInterfaceNil() bool {
	return tr == nil
}

// GetObsoleteHashes resets the oldHashes and oldRoot variables and returns the old hashes
func (tr *patriciaMerkleTrie) GetObsoleteHashes() [][]byte {
	tr.mutOperation.Lock()
	oldHashes := tr.oldHashes
	logArrayWithTrace("old trie hash", "hash", oldHashes)

	tr.mutOperation.Unlock()

	return oldHashes
}

// GetDirtyHashes returns all the dirty hashes from the trie
func (tr *patriciaMerkleTrie) GetDirtyHashes() (common.ModifiedHashes, error) {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	if tr.root == nil {
		return nil, nil
	}

	err := tr.root.setRootHash()
	if err != nil {
		return nil, err
	}

	dirtyHashes := make(common.ModifiedHashes)
	err = tr.root.getDirtyHashes(dirtyHashes)
	if err != nil {
		return nil, err
	}

	logMapWithTrace("new trie hash", "hash", dirtyHashes)

	return dirtyHashes, nil
}

func (tr *patriciaMerkleTrie) recreateFromDb(rootHash []byte, tsm common.StorageManager) (*patriciaMerkleTrie, snapshotNode, error) {
	newTr, err := NewTrie(
		tsm,
		tr.marshalizer,
		tr.hasher,
		tr.enableEpochsHandler,
		tr.maxTrieLevelInMemory,
	)
	if err != nil {
		return nil, nil, err
	}

	newRoot, err := getNodeFromDBAndDecode(rootHash, tsm, tr.marshalizer, tr.hasher)
	if err != nil {
		return nil, nil, err
	}

	newRoot.setGivenHash(rootHash)
	newTr.root = newRoot

	return newTr, newRoot, nil
}

// GetSerializedNode returns the serialized node (if existing) provided the node's hash
func (tr *patriciaMerkleTrie) GetSerializedNode(hash []byte) ([]byte, error) {
	// TODO: investigate if we can move the critical section behavior in the trie node resolver as this call will compete with a normal trie.Get operation
	//  which might occur during processing.
	//  warning: A critical section here or on the trie node resolver must be kept as to not overwhelm the node with requests that affects the block processing flow
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	log.Trace("GetSerializedNode", "hash", hash)

	return tr.trieStorage.Get(hash)
}

// GetSerializedNodes returns a batch of serialized nodes from the trie, starting from the given hash
func (tr *patriciaMerkleTrie) GetSerializedNodes(rootHash []byte, maxBuffToSend uint64) ([][]byte, uint64, error) {
	// TODO: investigate if we can move the critical section behavior in the trie node resolver as this call will compete with a normal trie.Get operation
	//  which might occur during processing.
	//  warning: A critical section here or on the trie node resolver must be kept as to not overwhelm the node with requests that affects the block processing flow
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	log.Trace("GetSerializedNodes", "rootHash", rootHash)
	size := uint64(0)

	newTr, _, err := tr.recreateFromDb(rootHash, tr.trieStorage)
	if err != nil {
		return nil, 0, err
	}

	it, err := NewDFSIterator(newTr)
	if err != nil {
		return nil, 0, err
	}

	encNode, err := it.MarshalizedNode()
	if err != nil {
		return nil, 0, err
	}

	nodes := make([][]byte, 0)
	nodes = append(nodes, encNode)
	size += uint64(len(encNode))

	for it.HasNext() {
		err = it.Next()
		if err != nil {
			return nil, 0, err
		}

		encNode, err = it.MarshalizedNode()
		if err != nil {
			return nil, 0, err
		}

		if size+uint64(len(encNode)) > maxBuffToSend {
			return nodes, 0, nil
		}
		nodes = append(nodes, encNode)
		size += uint64(len(encNode))
	}

	remainingSpace := maxBuffToSend - size

	return nodes, remainingSpace, nil
}

// GetAllLeavesOnChannel adds all the trie leaves to the given channel
func (tr *patriciaMerkleTrie) GetAllLeavesOnChannel(
	leavesChannels *common.TrieIteratorChannels,
	ctx context.Context,
	rootHash []byte,
	keyBuilder common.KeyBuilder,
	trieLeafParser common.TrieLeafParser,
) error {
	if leavesChannels == nil {
		return ErrNilTrieIteratorChannels
	}
	if leavesChannels.LeavesChan == nil {
		return ErrNilTrieIteratorLeavesChannel
	}
	if leavesChannels.ErrChan == nil {
		return ErrNilTrieIteratorErrChannel
	}
	if check.IfNil(keyBuilder) {
		return ErrNilKeyBuilder
	}
	if check.IfNil(trieLeafParser) {
		return ErrNilTrieLeafParser
	}

	newTrie, err := tr.recreate(rootHash, tr.trieStorage)
	if err != nil {
		close(leavesChannels.LeavesChan)
		leavesChannels.ErrChan.Close()
		return err
	}

	if check.IfNil(newTrie) || newTrie.root == nil {
		close(leavesChannels.LeavesChan)
		leavesChannels.ErrChan.Close()
		return nil
	}

	tr.trieStorage.EnterPruningBufferingMode()

	go func() {
		err = newTrie.root.getAllLeavesOnChannel(
			leavesChannels.LeavesChan,
			keyBuilder,
			trieLeafParser,
			tr.trieStorage,
			tr.marshalizer,
			tr.chanClose,
			ctx,
		)
		if err != nil {
			leavesChannels.ErrChan.WriteInChanNonBlocking(err)
			log.Error("could not get all trie leaves: ", "error", err)
		}

		tr.trieStorage.ExitPruningBufferingMode()

		close(leavesChannels.LeavesChan)
		leavesChannels.ErrChan.Close()
	}()

	return nil
}

// GetAllHashes returns all the hashes from the trie
func (tr *patriciaMerkleTrie) GetAllHashes() ([][]byte, error) {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	hashes := make([][]byte, 0)
	if tr.root == nil {
		return hashes, nil
	}

	err := tr.root.setRootHash()
	if err != nil {
		return nil, err
	}

	hashes, err = tr.root.getAllHashes(tr.trieStorage)
	if err != nil {
		return nil, err
	}

	return hashes, nil
}

func logArrayWithTrace(message string, paramName string, hashes [][]byte) {
	if log.GetLevel() == logger.LogTrace {
		for _, hash := range hashes {
			log.Trace(message, paramName, hash)
		}
	}
}

func logMapWithTrace(message string, paramName string, hashes common.ModifiedHashes) {
	if log.GetLevel() == logger.LogTrace {
		for key := range hashes {
			log.Trace(message, paramName, []byte(key))
		}
	}
}

// GetProof computes a Merkle proof for the node that is present at the given key
func (tr *patriciaMerkleTrie) GetProof(key []byte) ([][]byte, []byte, error) {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	if tr.root == nil {
		return nil, nil, ErrNilNode
	}

	var proof [][]byte
	hexKey := keyBytesToHex(key)
	currentNode := tr.root

	err := currentNode.setRootHash()
	if err != nil {
		return nil, nil, err
	}

	for {
		encodedNode, errGet := currentNode.getEncodedNode()
		if errGet != nil {
			return nil, nil, errGet
		}
		proof = append(proof, encodedNode)
		value := currentNode.getValue()

		currentNode, hexKey, errGet = currentNode.getNext(hexKey, tr.trieStorage)
		if errGet != nil {
			return nil, nil, errGet
		}

		if currentNode == nil {
			return proof, value, nil
		}
	}
}

// VerifyProof verifies the given Merkle proof
func (tr *patriciaMerkleTrie) VerifyProof(rootHash []byte, key []byte, proof [][]byte) (bool, error) {
	tr.mutOperation.RLock()
	defer tr.mutOperation.RUnlock()

	ok, err := tr.verifyProof(rootHash, tr.hasher.Compute(string(key)), proof)
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}

	return tr.verifyProof(rootHash, key, proof)
}

func (tr *patriciaMerkleTrie) verifyProof(rootHash []byte, key []byte, proof [][]byte) (bool, error) {
	wantHash := rootHash
	key = keyBytesToHex(key)
	for _, encodedNode := range proof {
		if encodedNode == nil {
			return false, nil
		}

		hash := tr.hasher.Compute(string(encodedNode))
		if !bytes.Equal(wantHash, hash) {
			return false, nil
		}

		n, errDecode := decodeNode(encodedNode, tr.marshalizer, tr.hasher)
		if errDecode != nil {
			return false, errDecode
		}

		var proofVerified bool
		proofVerified, wantHash, key = n.getNextHashAndKey(key)
		if proofVerified {
			return true, nil
		}
	}

	return false, nil
}

// GetStorageManager returns the storage manager for the trie
func (tr *patriciaMerkleTrie) GetStorageManager() common.StorageManager {
	return tr.trieStorage
}

// GetOldRoot returns the rootHash of the trie before the latest changes
func (tr *patriciaMerkleTrie) GetOldRoot() []byte {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	return tr.oldRoot
}

// GetTrieStats will collect and return the statistics for the given rootHash
func (tr *patriciaMerkleTrie) GetTrieStats(address string, rootHash []byte) (common.TrieStatisticsHandler, error) {
	newTrie, err := tr.recreate(rootHash, tr.trieStorage)
	if err != nil {
		return nil, err
	}

	ts := statistics.NewTrieStatistics()
	err = newTrie.root.collectStats(ts, rootDepthLevel, newTrie.trieStorage)
	if err != nil {
		return nil, err
	}
	ts.AddAccountInfo(address, rootHash)

	return ts, nil
}

// CollectLeavesForMigration will collect trie leaves that need to be migrated. The leaves are collected in the trieMigrator.
// The traversing of the trie is done in a DFS manner, and it will stop when the gas runs out (this will be signaled by the trieMigrator).
func (tr *patriciaMerkleTrie) CollectLeavesForMigration(args vmcommon.ArgsMigrateDataTrieLeaves) error {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	if check.IfNil(tr.root) {
		return nil
	}
	if check.IfNil(args.TrieMigrator) {
		return errors.ErrNilTrieMigrator
	}

	err := tr.checkIfMigrationPossible(args)
	if err != nil {
		return err
	}

	_, err = tr.root.collectLeavesForMigration(args, tr.trieStorage, keyBuilder.NewKeyBuilder())
	if err != nil {
		return err
	}

	return nil
}

func (tr *patriciaMerkleTrie) checkIfMigrationPossible(args vmcommon.ArgsMigrateDataTrieLeaves) error {
	if !tr.trieNodeVersionVerifier.IsValidVersion(args.NewVersion) {
		return fmt.Errorf("%w: newVersion %v", errors.ErrInvalidTrieNodeVersion, args.NewVersion)
	}

	if !tr.trieNodeVersionVerifier.IsValidVersion(args.OldVersion) {
		return fmt.Errorf("%w: oldVersion %v", errors.ErrInvalidTrieNodeVersion, args.OldVersion)
	}

	if args.NewVersion == core.NotSpecified && args.OldVersion == core.AutoBalanceEnabled {
		return fmt.Errorf("%w: cannot migrate from %v to %v", errors.ErrInvalidTrieNodeVersion, core.AutoBalanceEnabled, core.NotSpecified)
	}

	return nil
}

// IsMigratedToLatestVersion returns true if the trie is migrated to the latest version
func (tr *patriciaMerkleTrie) IsMigratedToLatestVersion() (bool, error) {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	if check.IfNil(tr.root) {
		return true, nil
	}

	version, err := tr.root.getVersion()
	if err != nil {
		return false, err
	}

	versionForNewlyAddedData := core.GetVersionForNewData(tr.enableEpochsHandler)
	return version == versionForNewlyAddedData, nil
}

// GetNodeDataFromHash returns the node data for the given hash
func GetNodeDataFromHash(hash []byte, keyBuilder common.KeyBuilder, db common.TrieStorageInteractor, msh marshal.Marshalizer, hsh hashing.Hasher) ([]common.TrieNodeData, error) {
	n, err := getNodeFromDBAndDecode(hash, db, msh, hsh)
	if err != nil {
		return nil, err
	}

	return n.getNodeData(keyBuilder)
}

// Close stops all the active goroutines started by the trie
func (tr *patriciaMerkleTrie) Close() error {
	tr.mutOperation.Lock()
	defer tr.mutOperation.Unlock()

	if !isChannelClosed(tr.chanClose) {
		close(tr.chanClose)
	}

	return nil
}

func isChannelClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
	}

	return false
}
