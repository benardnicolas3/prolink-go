package prolink

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"time"
	"unicode/utf16"
)

// ErrDeviceNotLinked is returned by RemoteDB if the device being queried is
// not currently 'linked' on the network.
var ErrDeviceNotLinked = fmt.Errorf("The device is not linked on the network")

// rdSeparator is a 6 byte marker used in TCP packets sent sent and received
// from the remote db server. It's not particular known exactly what this
// value is for, but in some packets it seems to be used as a field separator.
var rdSeparator = []byte{0x11, 0x87, 0x23, 0x49, 0xae, 0x11}

// buildPacket constructs a packet to be sent to remote database.
func buildPacket(messageID uint32, part []byte) []byte {
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, messageID)

	header := bytes.Join([][]byte{rdSeparator, count}, nil)

	return append(header, part...)
}

// Given a byte array where the first 4 bytes contain the uint32 length of the
// string (number of runes) followed by a UTF-16 representation of the string,
// convert it to a string.
func stringFromUTF16(s []byte) string {
	size := binary.BigEndian.Uint32(s[:4])
	s = s[4:][:size*2]

	str16Bit := make([]uint16, 0, size)
	for ; len(s) > 0; s = s[2:] {
		str16Bit = append(str16Bit, binary.BigEndian.Uint16(s[:2]))
	}

	return string(utf16.Decode(str16Bit))[:size-1]
}

// rbDBServerQueryPort is the consistent port on which we can query the remote
// db server for the port to connect to to communicate with it.
const rbDBServerQueryPort = 12523

// getRemoteDBServerAddr queries the remote device for the port that the remote
// database server is listening on for requests.
func getRemoteDBServerAddr(deviceIP net.IP) (string, error) {
	addr := fmt.Sprintf("%s:%d", deviceIP, rbDBServerQueryPort)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return "", err
	}

	defer conn.Close()

	parts := [][]byte{
		[]byte{0x00, 0x00, 0x00, 0x0f},
		[]byte("RemoteDBServer"),
		[]byte{0x00},
	}

	queryPacket := bytes.Join(parts, nil)

	// Request for the port
	_, err = conn.Write(queryPacket)
	if err != nil {
		return "", fmt.Errorf("Failed to query remote DB Server port: %s", err)
	}

	// Read request response, should be a two byte uint16
	data := make([]byte, 2)

	_, err = conn.Read(data)
	if err != nil {
		return "", fmt.Errorf("Failed to retrieve remote DB Server port: %s", err)
	}

	port := binary.BigEndian.Uint16(data)

	return fmt.Sprintf("%s:%d", deviceIP, port), nil
}

// Track contains track information retrieved from the remote database.
type Track struct {
	ID      uint32
	Path    string
	Title   string
	Artist  string
	Album   string
	Label   string
	Genre   string
	Comment string
	Key     string
	Length  time.Duration
	Artwork []byte
}

// RemoteDB provides an interface to talking to the remote database.
type RemoteDB struct {
	conn     net.Conn
	deviceID DeviceID
	msgCount uint32
}

// OnLink adds a handler to be triggered when the DB server becomes available
// on the network.
func (rd *RemoteDB) OnLink() {
	// TODO
}

// IsLinked reports weather the  DB server is available on the network.
func (rd *RemoteDB) IsLinked() bool {
	return rd.conn != nil
}

// GetTrack queries the remote db for track details given a track ID.
func (rd *RemoteDB) GetTrack(id uint32) (*Track, error) {
	if rd.conn == nil {
		return nil, ErrDeviceNotLinked
	}

	track, err := rd.queryTrackMetadata(id)
	if err != nil {
		return nil, err
	}

	path, err := rd.queryTrackPath(id)
	if err != nil {
		return nil, err
	}

	track.Path = path

	// No artwork, nothing left to do
	if binary.BigEndian.Uint32(track.Artwork) == 0 {
		return track, nil
	}

	artID := binary.BigEndian.Uint32(track.Artwork)

	artwork, err := rd.queryArtwork(artID)
	if err != nil {
		return nil, err
	}

	track.Artwork = artwork

	return track, nil
}

// queryTrackMetadata queries the rmote database for various metadata about a
// track, returing a sparse Track object. The track Path and Artwork must be
// looked up as separate queries.
//
// Note that the Artwork ID is populated in the Artwork field, as this value is
// returned with the track metadata and is needed to lookup the artwork.
func (rd *RemoteDB) queryTrackMetadata(id uint32) (*Track, error) {
	trackID := make([]byte, 4)
	binary.BigEndian.PutUint32(trackID, id)

	dvID := byte(rd.deviceID)
	slot := byte(TrackSlotRB)

	part1 := []byte{
		0x10, 0x20, 0x02, 0x0f, 0x02, 0x14, 0x00, 0x00,
		0x00, 0x0c, 0x06, 0x06, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x11, dvID,
		0x01, slot, 0x01, 0x11,
	}
	part1 = append(part1, trackID...)

	part2 := []byte{
		0x10, 0x30, 0x00, 0x0f, 0x06, 0x14, 0x00, 0x00,
		0x00, 0x0c, 0x06, 0x06, 0x06, 0x06, 0x06, 0x06,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x11, dvID,
		0x01, slot, 0x01, 0x11, 0x00, 0x00, 0x00, 0x00,
		0x11, 0x00, 0x00, 0x00, 0x0b, 0x11, 0x00, 0x00,
		0x00, 0x00, 0x11, 0x00, 0x00, 0x00, 0x0b, 0x11,
		0x00, 0x00, 0x00, 0x00,
	}

	items, err := rd.getMultimessageResp(part1, part2)
	if err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(items[3][28:32])

	track := &Track{
		ID:      id,
		Title:   stringFromUTF16(items[0][38:]),
		Artist:  stringFromUTF16(items[1][38:]),
		Album:   stringFromUTF16(items[2][38:]),
		Comment: stringFromUTF16(items[5][38:]),
		Key:     stringFromUTF16(items[6][38:]),
		Genre:   stringFromUTF16(items[9][38:]),
		Label:   stringFromUTF16(items[10][38:]),
		Length:  time.Duration(length) * time.Second,

		// Artwork will be given in with the artwork ID
		Artwork: items[0][len(items[0])-19:][:4],
	}

	return track, nil

}

// queryTrackPath looks up the file path of a track in rekordbox.
func (rd *RemoteDB) queryTrackPath(id uint32) (string, error) {
	trackID := make([]byte, 4)
	binary.BigEndian.PutUint32(trackID, id)

	dvID := byte(rd.deviceID)

	part1 := []byte{
		0x10, 0x21, 0x02, 0x0f, 0x02, 0x14, 0x00, 0x00,
		0x00, 0x0c, 0x06, 0x06, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x11, dvID,
		0x08, 0x04, 0x01, 0x11,
	}
	part1 = append(part1, trackID...)

	part2 := []byte{
		0x10, 0x30, 0x00, 0x0f, 0x06, 0x14, 0x00, 0x00,
		0x00, 0x0c, 0x06, 0x06, 0x06, 0x06, 0x06, 0x06,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x11, dvID,
		0x08, 0x04, 0x01, 0x11, 0x00, 0x00, 0x00, 0x00,
		0x11, 0x00, 0x00, 0x00, 0x06, 0x11, 0x00, 0x00,
		0x00, 0x00, 0x11, 0x00, 0x00, 0x00, 0x06, 0x11,
		0x00, 0x00, 0x00, 0x00,
	}

	items, err := rd.getMultimessageResp(part1, part2)
	if err != nil {
		return "", err
	}

	return stringFromUTF16(items[4][38:]), nil
}

// getMultimessageResp is used for queries that that multiple packets to setup
// and respond with mult-section bodies that can be split on the rbSection
// delimiter.
func (rd *RemoteDB) getMultimessageResp(p1, p2 []byte) ([][]byte, error) {
	// Part one of query
	packet := buildPacket(rd.msgCount, p1)

	if err := rd.sendMessage(packet); err != nil {
		return nil, fmt.Errorf("Multipart query failed: %s", err)
	}

	// This data doesn't seem useful, there *should* be 42 bytes of it
	io.CopyN(ioutil.Discard, rd.conn, 42)

	// Part two of query
	packet = buildPacket(rd.msgCount, p2)

	// As far as I can tell, these multi-section packets *do not* have a length
	// marker for bytes in the message, or even how many sections they will
	// have. So for now, look for the 'final section' which seems to always be
	// empty. We can reuse buildPacket here even though this is not a packet.
	finalSection := buildPacket(rd.msgCount, []byte{
		0x10, 0x42, 0x01, 0x0f, 0x00, 0x14, 0x00, 0x00, 0x00,
		0x0c, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	})

	if err := rd.sendMessage(packet); err != nil {
		return nil, fmt.Errorf("Multipart query failed: %s", err)
	}

	part := make([]byte, 1024)
	full := []byte{}

	for !bytes.HasSuffix(full, finalSection) {
		n, err := rd.conn.Read(part)
		if err != nil {
			return nil, fmt.Errorf("Could not read multipart response: %s", err)
		}

		full = append(full, part[:n]...)
	}

	// Break into sections (keep only interesting ones
	sections := bytes.Split(full, rdSeparator)[2:]
	sections = sections[:len(sections)-1]

	// Remove uint32 message counter from each section
	for i := range sections {
		sections[i] = sections[i][4:]
	}

	return sections, nil
}

// queryArtwork requests artwork of a specific ID from the remote database.
func (rd *RemoteDB) queryArtwork(id uint32) ([]byte, error) {
	artID := make([]byte, 4)
	binary.BigEndian.PutUint32(artID, id)

	dvID := byte(rd.deviceID)

	part := []byte{
		0x10, 0x20, 0x03, 0x0f, 0x02, 0x14, 0x00, 0x00,
		0x00, 0x0c, 0x06, 0x06, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x11, dvID,
		0x08, 0x04, 0x01, 0x11,
	}
	part = append(part, artID...)

	packet := buildPacket(rd.msgCount, part)

	if err := rd.sendMessage(packet); err != nil {
		return nil, fmt.Errorf("Artwork query failed: %s", err)
	}

	// there is a uint32 at byte 48 containing the size of the image, simply
	// read up until this value so we know how much more to read after.
	data := make([]byte, 52)

	_, err := rd.conn.Read(data)
	if err != nil {
		return nil, err
	}

	imgLen := binary.BigEndian.Uint32(data[48:52])
	img := make([]byte, int(imgLen))

	_, err = io.ReadFull(rd.conn, img)
	if err != nil {
		return nil, fmt.Errorf("Failed to read artwork data stream: %s", err)
	}

	return img, nil
}

// sendMessage writes to the open connection and increments the message
// counter.
func (rd *RemoteDB) sendMessage(m []byte) error {
	if _, err := rd.conn.Write(m); err != nil {
		return err
	}

	rd.msgCount++

	return nil
}

// openConnection initializes the TCP connection to the specified address on
// which the remote database is presumed to be running. This sends the
// appropriate packets to initialize the communication between a fake device
// (this host) and the remote database.
func (rd *RemoteDB) openConnection(addr string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}

	// Begin connection to the remote database
	if _, err = conn.Write([]byte{0x11, 0x00, 0x00, 0x00, 0x01}); err != nil {
		return fmt.Errorf("Failed to connect to remote database: %s", err)
	}

	// No need to keep this response, but it *should* be 5 bytes
	io.CopyN(ioutil.Discard, conn, 5)

	// Send identification to the remote database
	identifyParts := [][]byte{
		rdSeparator,

		// Possibly resets the
		[]byte{0xff, 0xff, 0xff, 0xfe},

		// Currently don't know what these bytes do, but they're needed to get
		// the connection into a state where we can make queries
		[]byte{
			0x10, 0x00, 0x00, 0x0f, 0x01, 0x14, 0x00, 0x00,
			0x00, 0x0c, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x11, 0x00,
			0x00, 0x00,
		},

		// The last byte of the identifier is the device ID that we are assuming
		// to use to communicate with the remote database
		[]byte{byte(rd.deviceID)},
	}

	if _, err = conn.Write(bytes.Join(identifyParts, nil)); err != nil {
		return fmt.Errorf("Failed to connect to remote database: %s", err)
	}

	// No need to keep this response, but it *should be 42 bytes
	io.CopyN(ioutil.Discard, conn, 42)

	rd.conn = conn
	rd.msgCount = 1

	return nil
}

// activate begins actively listening for devices on the network hat support
// remote database queries to be added to the PRO DJ LINK network. This
// maintains adding and removing of device connections.
func (rd *RemoteDB) activate(dm *DeviceManager, deviceID DeviceID) {
	rd.deviceID = deviceID

	// TODO: This isn't robust at all, handle polling for the DB server, since
	// it won't always be available OR figure out how to tell when it does
	// become available.
	dm.OnDeviceAdded(func(dev *Device) {
		if dev.Type != DeviceTypeRB {
			return
		}

		addr, err := getRemoteDBServerAddr(net.ParseIP("192.168.1.3"))
		if err != nil {
			fmt.Println(err)
			return
		}

		rd.openConnection(addr)
	})
}

func newRemoteDB() *RemoteDB {
	return &RemoteDB{}
}