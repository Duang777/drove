package workspace

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

type removalAcknowledgementReservationCandidate struct {
	name    string
	id      uuid.UUID
	renamed bool
}

type removalAcknowledgementReservation struct {
	bucket *os.Root
	root   *os.Root
	record workspaceRecord
	name   string
}

func removalAcknowledgementReservationPrefix(
	record workspaceRecord,
) string {
	return "." + record.AgentID + workspaceRecordSuffix + ".ack-path-" +
		record.Removal.OperationID + "-"
}

func removalAcknowledgementReservationName(
	record workspaceRecord,
) string {
	return removalAcknowledgementReservationPrefix(record) + uuid.NewString()
}

func (m *Manager) reserveRemovalAcknowledgementPath(
	record workspaceRecord,
) (_ *removalAcknowledgementReservation, result error) {
	if record.Removal == nil ||
		!record.Removal.Started ||
		record.Removal.OperationID == "" ||
		record.Removal.DirectoryToken == "" {
		return nil, errors.New(
			"workspace: completed removal has no acknowledgement reservation identity",
		)
	}
	bucket, err := m.openManagedBucketRoot(record.workspace())
	if err != nil {
		return nil, err
	}
	bucketOwned := true
	defer func() {
		if bucketOwned {
			result = errors.Join(result, bucket.Close())
		}
	}()
	if err := cleanupRemovalAcknowledgementReservations(bucket, record); err != nil {
		return nil, err
	}

	info, err := bucket.Lstat(record.AgentID)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf(
				"workspace: managed path %q appeared before removal acknowledgement",
				record.Path,
			)
		}
		root, err := openRealRootFromRoot(bucket, record.AgentID)
		if err != nil {
			return nil, err
		}
		reservation := &removalAcknowledgementReservation{
			bucket: bucket,
			root:   root,
			record: record,
			name:   record.AgentID,
		}
		if err := reservation.verify(); err != nil {
			_ = root.Close()
			return nil, fmt.Errorf(
				"workspace: managed path %q appeared before removal acknowledgement: %w",
				record.Path,
				err,
			)
		}
		bucketOwned = false
		return reservation, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf(
			"workspace: inspect removal acknowledgement path %q: %w",
			record.Path,
			err,
		)
	}

	stagingName := removalAcknowledgementReservationName(record)
	if err := bucket.Mkdir(stagingName, 0o700); err != nil {
		return nil, fmt.Errorf(
			"workspace: create removal acknowledgement reservation: %w",
			err,
		)
	}
	root, err := openRealRootFromRoot(bucket, stagingName)
	if err != nil {
		return nil, fmt.Errorf(
			"workspace: open removal acknowledgement reservation: %w",
			err,
		)
	}
	reservation := &removalAcknowledgementReservation{
		bucket: bucket,
		root:   root,
		record: record,
		name:   stagingName,
	}
	reservationOwned := true
	defer func() {
		if reservationOwned {
			result = errors.Join(result, reservation.release())
		}
	}()
	if err := installRemovalMarker(
		root,
		removalMarkerName(record),
		record.Removal.DirectoryToken,
	); err != nil {
		return nil, err
	}
	if err := reservation.verify(); err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	directory, err := openRecordDirectory(bucket)
	if err != nil {
		return nil, err
	}
	moved, renameErr := renameDirectoryNoReplace(
		directory,
		opened,
		stagingName,
		removalQuarantineIsolationName(stagingName),
		record.AgentID,
	)
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if moved {
		reservation.name = record.AgentID
	}
	if err := errors.Join(renameErr, syncErr, closeErr); err != nil {
		return nil, fmt.Errorf(
			"workspace: install removal acknowledgement reservation: %w",
			err,
		)
	}
	if !moved {
		return nil, errors.New(
			"workspace: removal acknowledgement path was not reserved",
		)
	}
	if err := reservation.verify(); err != nil {
		return nil, err
	}
	reservationOwned = false
	bucketOwned = false
	return reservation, nil
}

func (r *removalAcknowledgementReservation) verify() error {
	if r == nil || r.bucket == nil || r.root == nil {
		return errors.New(
			"workspace: removal acknowledgement reservation is closed",
		)
	}
	onlyMarker, err := removalDirectoryContainsOnlyMarker(r.root, r.record)
	if err != nil {
		return err
	}
	if !onlyMarker {
		return errors.New(
			"workspace: removal acknowledgement reservation contains unexpected entries",
		)
	}
	return verifyRootEntryUnchanged(r.bucket, r.name, r.root)
}

func (r *removalAcknowledgementReservation) release() (result error) {
	if r == nil {
		return nil
	}
	if r.bucket == nil {
		return nil
	}
	defer func() {
		if r.root != nil {
			result = errors.Join(result, r.root.Close())
			r.root = nil
		}
		result = errors.Join(result, r.bucket.Close())
		r.bucket = nil
	}()
	if r.root == nil {
		return errors.New(
			"workspace: removal acknowledgement reservation root is closed",
		)
	}
	if r.name == r.record.AgentID {
		if err := r.verify(); err != nil {
			return err
		}
		opened, err := r.root.Stat(".")
		if err != nil {
			return err
		}
		isolatedName := removalAcknowledgementReservationName(r.record)
		directory, err := openRecordDirectory(r.bucket)
		if err != nil {
			return err
		}
		moved, renameErr := renameDirectoryNoReplace(
			directory,
			opened,
			r.name,
			removalQuarantineIsolationName(isolatedName),
			isolatedName,
		)
		syncErr := syncRecordDirectory(directory)
		closeErr := directory.Close()
		if moved {
			r.name = isolatedName
		}
		if err := errors.Join(renameErr, syncErr, closeErr); err != nil {
			return fmt.Errorf(
				"workspace: isolate removal acknowledgement reservation: %w",
				err,
			)
		}
		if !moved {
			return errors.New(
				"workspace: removal acknowledgement reservation was not isolated",
			)
		}
	} else {
		if err := validateClearedRemovalDirectory(
			r.root,
			r.record,
			false,
		); err != nil {
			return err
		}
		if err := verifyRootEntryUnchanged(
			r.bucket,
			r.name,
			r.root,
		); err != nil {
			return err
		}
	}
	if err := removeRemovalMarkerIfPresent(r.root, r.record); err != nil {
		return err
	}
	if err := validateClearedRemovalDirectory(
		r.root,
		r.record,
		false,
	); err != nil {
		return err
	}
	if err := verifyRootEntryUnchanged(r.bucket, r.name, r.root); err != nil {
		return err
	}
	if err := r.root.Close(); err != nil {
		r.root = nil
		return err
	}
	r.root = nil
	if err := r.bucket.Remove(r.name); err != nil {
		return err
	}
	return syncRecordBucket(r.bucket, r.record.Path)
}

func cleanupRemovalAcknowledgementReservations(
	bucket *os.Root,
	record workspaceRecord,
) error {
	entries, err := readRootDirectory(bucket)
	if err != nil {
		return err
	}
	prefix := removalAcknowledgementReservationPrefix(record)
	candidates := make(
		[]removalAcknowledgementReservationCandidate,
		0,
	)
	seen := make(map[uuid.UUID]removalAcknowledgementReservationCandidate)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		candidate, valid := parseRemovalAcknowledgementReservationName(
			entry.Name(),
			prefix,
		)
		if !valid {
			return fmt.Errorf(
				"workspace: removal acknowledgement reservation %q has an invalid name",
				entry.Name(),
			)
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf(
				"workspace: removal acknowledgement reservation %q is not a real directory",
				entry.Name(),
			)
		}
		if previous, exists := seen[candidate.id]; exists {
			return fmt.Errorf(
				"workspace: removal acknowledgement reservations %q and %q have ambiguous phases",
				previous.name,
				candidate.name,
			)
		}
		seen[candidate.id] = candidate
		candidates = append(candidates, candidate)
	}
	for _, candidate := range candidates {
		root, err := openRealRootFromRoot(bucket, candidate.name)
		if err != nil {
			return err
		}
		reservation := &removalAcknowledgementReservation{
			bucket: bucket,
			root:   root,
			record: record,
			name:   candidate.name,
		}
		if err := cleanupIsolatedRemovalAcknowledgementReservation(
			reservation,
		); err != nil {
			_ = root.Close()
			return err
		}
	}
	return nil
}

func cleanupIsolatedRemovalAcknowledgementReservation(
	reservation *removalAcknowledgementReservation,
) error {
	if err := cleanupRemovalMarkerTemps(
		reservation.root,
		reservation.record,
	); err != nil {
		return err
	}
	if err := validateClearedRemovalDirectory(
		reservation.root,
		reservation.record,
		false,
	); err != nil {
		return err
	}
	if err := removeRemovalMarkerIfPresent(
		reservation.root,
		reservation.record,
	); err != nil {
		return err
	}
	if err := verifyRootEntryUnchanged(
		reservation.bucket,
		reservation.name,
		reservation.root,
	); err != nil {
		return err
	}
	if err := reservation.root.Close(); err != nil {
		reservation.root = nil
		return err
	}
	reservation.root = nil
	if err := reservation.bucket.Remove(reservation.name); err != nil {
		return err
	}
	return syncRecordBucket(reservation.bucket, reservation.record.Path)
}

func parseRemovalAcknowledgementReservationName(
	name string,
	prefix string,
) (removalAcknowledgementReservationCandidate, bool) {
	raw, found := strings.CutPrefix(name, prefix)
	if !found {
		return removalAcknowledgementReservationCandidate{}, false
	}
	const renameSuffix = ".rename"
	renamed := strings.HasSuffix(raw, renameSuffix)
	if renamed {
		raw = strings.TrimSuffix(raw, renameSuffix)
	}
	if len(raw) != 36 {
		return removalAcknowledgementReservationCandidate{}, false
	}
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != raw {
		return removalAcknowledgementReservationCandidate{}, false
	}
	return removalAcknowledgementReservationCandidate{
		name:    name,
		id:      parsed,
		renamed: renamed,
	}, true
}

func removalPathIsAcknowledgementReservation(
	bucket *os.Root,
	record workspaceRecord,
) (_ bool, result error) {
	if record.Removal == nil || !record.Removal.Started {
		return false, nil
	}
	root, err := openRealRootFromRoot(bucket, record.AgentID)
	if err != nil {
		return false, err
	}
	defer func() {
		result = errors.Join(result, root.Close())
	}()
	return openedRemovalPathIsAcknowledgementReservation(
		bucket,
		root,
		record,
	)
}

func openedRemovalPathIsAcknowledgementReservation(
	bucket *os.Root,
	root *os.Root,
	record workspaceRecord,
) (bool, error) {
	if record.Removal == nil || !record.Removal.Started {
		return false, nil
	}
	onlyMarker, err := removalDirectoryContainsOnlyMarker(root, record)
	if err != nil {
		return false, err
	}
	if !onlyMarker {
		return false, nil
	}
	if err := verifyRootEntryUnchanged(bucket, record.AgentID, root); err != nil {
		return false, err
	}
	return true, nil
}
