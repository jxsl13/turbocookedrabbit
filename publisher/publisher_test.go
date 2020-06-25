package publisher_test

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/stretchr/testify/assert"

	"github.com/houseofcat/turbocookedrabbit/models"
	"github.com/houseofcat/turbocookedrabbit/pools"
	"github.com/houseofcat/turbocookedrabbit/publisher"
	"github.com/houseofcat/turbocookedrabbit/topology"
	"github.com/houseofcat/turbocookedrabbit/utils"
)

var Seasoning *models.RabbitSeasoning
var ConnectionPool *pools.ConnectionPool
var ChannelPool *pools.ChannelPool

func TestMain(m *testing.M) { // Load Configuration On Startup
	var err error
	Seasoning, err = utils.ConvertJSONFileToConfig("testpublisherseasoning.json")
	if err != nil {
		fmt.Print(err.Error())
		return
	}

	ConnectionPool, err = pools.NewConnectionPool(Seasoning.PoolConfig, true)
	if err != nil {
		fmt.Print(err.Error())
		return
	}

	ChannelPool, err = pools.NewChannelPool(Seasoning.PoolConfig, ConnectionPool, true)
	if err != nil {
		fmt.Print(err.Error())
		return
	}

	os.Exit(m.Run())
}

func TestCreatePublisher(t *testing.T) {
	channelPool, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	publisher, err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, err)
	assert.NotNil(t, publisher)
}

func TestCreatePublisherAndPublish(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	channelPool, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	publisher, err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, err)
	assert.NotNil(t, publisher)

	letterID := uint64(1)
	body := "\xFF\xFF\x89\xFF\xFF"
	envelope := &models.Envelope{
		Exchange:    "",
		RoutingKey:  "TestQueue",
		ContentType: "plain/text",
		Mandatory:   false,
		Immediate:   false,
	}

	letter := &models.Letter{
		LetterID:   letterID,
		RetryCount: uint32(3),
		Body:       []byte(body),
		Envelope:   envelope,
	}

	publisher.Publish(letter)

	// Assert on all Notifications
AssertLoop:
	for {
		select {
		case notification := <-publisher.Notifications():
			assert.True(t, notification.Success)
			assert.Equal(t, letterID, notification.LetterID)
			assert.NoError(t, notification.Error)
			break AssertLoop
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	channelPool.Shutdown()
}

func TestAutoPublishSingleMessage(t *testing.T) {

	channelPool, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	publisher, err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, err)

	letter := utils.CreateMockRandomLetter("ConsumerTestQueue")

	publisher.StartAutoPublish()

	publisher.QueueLetter(letter)

	publisher.StopAutoPublish()

	// Assert on all Notifications
AssertLoop:
	for {
		select {
		case notification := <-publisher.Notifications():
			assert.True(t, notification.Success)
			assert.Equal(t, letter.LetterID, notification.LetterID)
			assert.NoError(t, notification.Error)
			break AssertLoop
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	channelPool.Shutdown()
}

func TestAutoPublishManyMessages(t *testing.T) {

	defer leaktest.Check(t)() // Fail on leaked goroutines.
	messageCount := 100000

	channelPool, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	// Purge all queues first.
	purgeAllPublisherTestQueues("PubTQ", channelPool)

	publisher, err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, err)

	// Pre-create test messages
	timeStart := time.Now()
	letters := make([]*models.Letter, messageCount)

	for i := 0; i < messageCount; i++ {
		letters[i] = utils.CreateMockLetter(uint64(i), "", fmt.Sprintf("PubTQ-%d", i%10), nil)
	}

	elapsed := time.Since(timeStart)
	fmt.Printf("Time Elapsed Creating Letters: %s\r\n", elapsed)

	timeStart = time.Now()
	publisher.StartAutoPublish()

	go func() {

		for _, letter := range letters {
			publisher.QueueLetter(letter)
		}
	}()

	successCount := 0
	failureCount := 0
	timer := time.NewTimer(1 * time.Minute)

ListeningForNotificationsLoop:
	for {
		select {
		case <-timer.C:
			break ListeningForNotificationsLoop
		case notification := <-publisher.Notifications():
			if notification.Success {
				successCount++
			} else {
				failureCount++
			}

			if successCount+failureCount == messageCount {
				break ListeningForNotificationsLoop
			}

			break
		default:
			time.Sleep(1 * time.Millisecond)
			break
		}
	}

	elapsed = time.Since(timeStart)

	assert.Equal(t, messageCount, successCount+failureCount)
	fmt.Printf("All Messages Accounted For: %d\r\n", successCount)
	fmt.Printf("Success Count: %d\r\n", successCount)
	fmt.Printf("Failure Count: %d\r\n", failureCount)
	fmt.Printf("Time Elapsed: %s\r\n", elapsed)
	fmt.Printf("Rate: %f msg/s\r\n", float64(messageCount)/elapsed.Seconds())

	// Purge all queues.
	purgeAllPublisherTestQueues("PubTQ", channelPool)

	// Shut down everything.
	publisher.StopAutoPublish()
	channelPool.Shutdown()
}

func TestTwoAutoPublishSameChannelPool(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	messageCount := 50000
	publisherMultiple := 2

	channelPool, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	// Purge all queues first.
	purgeAllPublisherTestQueues("PubTQ", channelPool)

	publisher1, p1Err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, p1Err)

	publisher2, p2Err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, p2Err)

	// Pre-create test messages
	timeStart := time.Now()
	letters := make([]*models.Letter, messageCount)

	for i := 0; i < messageCount; i++ {
		letters[i] = utils.CreateMockLetter(uint64(i), "", fmt.Sprintf("PubTQ-%d", i%10), nil)
	}

	elapsed := time.Since(timeStart)
	fmt.Printf("Time Elapsed Creating Letters: %s\r\n", elapsed)

	timeStart = time.Now()
	publisher1.StartAutoPublish()
	publisher2.StartAutoPublish()

	go func() {

		for _, letter := range letters {
			publisher1.QueueLetter(letter)
			publisher2.QueueLetter(letter)
		}
	}()

	successCount := 0
	failureCount := 0
	timer := time.NewTimer(1 * time.Minute)

	var notification *models.Notification
ListeningForNotificationsLoop:
	for {
		select {
		case <-timer.C:
			break ListeningForNotificationsLoop
		case notification = <-publisher1.Notifications():
		case notification = <-publisher2.Notifications():
		default:
			time.Sleep(1 * time.Millisecond)
			break
		}

		if notification != nil {
			if notification.Success {
				successCount++
			} else {
				failureCount++
			}

			notification = nil
		}

		if successCount+failureCount == publisherMultiple*messageCount {
			break ListeningForNotificationsLoop
		}
	}

	elapsed = time.Since(timeStart)

	assert.Equal(t, publisherMultiple*messageCount, successCount+failureCount)
	fmt.Printf("All Messages Accounted For: %d\r\n", successCount)
	fmt.Printf("Success Count: %d\r\n", successCount)
	fmt.Printf("Failure Count: %d\r\n", failureCount)
	fmt.Printf("Time Elapsed: %s\r\n", elapsed)
	fmt.Printf("Rate: %f msg/s\r\n", float64(publisherMultiple*messageCount)/elapsed.Seconds())

	// Purge all queues.
	purgeAllPublisherTestQueues("PubTQ", channelPool)

	// Shut down everything.
	publisher1.StopAutoPublish()
	publisher2.StopAutoPublish()
	channelPool.Shutdown()
}

func TestFourAutoPublishSameChannelPool(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	messageCount := 50000
	publisherMultiple := 4

	channelPool, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	// Purge all queues first.
	purgeAllPublisherTestQueues("PubTQ", ChannelPool)

	publisher1, p1Err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, p1Err)

	publisher2, p2Err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, p2Err)

	publisher3, p3Err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, p3Err)

	publisher4, p4Err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, p4Err)

	// Pre-create test messages
	timeStart := time.Now()
	letters := make([]*models.Letter, messageCount)

	for i := 0; i < messageCount; i++ {
		letters[i] = utils.CreateMockLetter(uint64(i), "", fmt.Sprintf("PubTQ-%d", i%10), nil)
	}

	elapsed := time.Since(timeStart)
	fmt.Printf("Time Elapsed Creating Letters: %s\r\n", elapsed)

	timeStart = time.Now()
	publisher1.StartAutoPublish()
	publisher2.StartAutoPublish()
	publisher3.StartAutoPublish()
	publisher4.StartAutoPublish()

	go func() {

		for _, letter := range letters {
			publisher1.QueueLetter(letter)
			publisher2.QueueLetter(letter)
			publisher3.QueueLetter(letter)
			publisher4.QueueLetter(letter)
		}
	}()

	successCount := 0
	failureCount := 0
	timer := time.NewTimer(1 * time.Minute)

	var notification *models.Notification
ListeningForNotificationsLoop:
	for {
		select {
		case <-timer.C:
			fmt.Printf(" == Timeout Occurred == ")
			break ListeningForNotificationsLoop
		case notification = <-publisher1.Notifications():
		case notification = <-publisher2.Notifications():
		case notification = <-publisher3.Notifications():
		case notification = <-publisher4.Notifications():
		default:
			time.Sleep(1 * time.Millisecond)
			break
		}

		if notification != nil {
			if notification.Success {
				successCount++
			} else {
				failureCount++
			}

			notification = nil
		}

		if successCount+failureCount == publisherMultiple*messageCount {
			break ListeningForNotificationsLoop
		}
	}

	elapsed = time.Since(timeStart)

	assert.Equal(t, publisherMultiple*messageCount, successCount+failureCount)
	fmt.Printf("All Messages Accounted For: %d\r\n", successCount)
	fmt.Printf("Success Count: %d\r\n", successCount)
	fmt.Printf("Failure Count: %d\r\n", failureCount)
	fmt.Printf("Time Elapsed: %s\r\n", elapsed)
	fmt.Printf("Rate: %f msg/s\r\n", float64(publisherMultiple*messageCount)/elapsed.Seconds())

	// Purge all queues.
	purgeAllPublisherTestQueues("PubTQ", ChannelPool)

	// Shut down everything.
	publisher1.StopAutoPublish()
	publisher2.StopAutoPublish()
	publisher3.StopAutoPublish()
	publisher4.StopAutoPublish()
	channelPool.Shutdown()
}

func TestFourAutoPublishFourChannelPool(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	messageCount := 50000
	publisherMultiple := 4

	channelPool1, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	channelPool2, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	channelPool3, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	channelPool4, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	// Purge all queues first.
	purgeAllPublisherTestQueues("PubTQ", ChannelPool)

	publisher1, p1Err := publisher.NewPublisher(Seasoning, channelPool1, nil)
	assert.NoError(t, p1Err)

	publisher2, p2Err := publisher.NewPublisher(Seasoning, channelPool2, nil)
	assert.NoError(t, p2Err)

	publisher3, p3Err := publisher.NewPublisher(Seasoning, channelPool3, nil)
	assert.NoError(t, p3Err)

	publisher4, p4Err := publisher.NewPublisher(Seasoning, channelPool4, nil)
	assert.NoError(t, p4Err)

	// Pre-create test messages
	timeStart := time.Now()
	letters := make([]*models.Letter, messageCount)

	for i := 0; i < messageCount; i++ {
		letters[i] = utils.CreateMockLetter(uint64(i), "", fmt.Sprintf("PubTQ-%d", i%10), nil)
	}

	elapsed := time.Since(timeStart)
	fmt.Printf("Time Elapsed Creating Letters: %s\r\n", elapsed)

	timeStart = time.Now()
	publisher1.StartAutoPublish()
	publisher2.StartAutoPublish()
	publisher3.StartAutoPublish()
	publisher4.StartAutoPublish()

	go func() {

		for _, letter := range letters {
			publisher1.QueueLetter(letter)
			publisher2.QueueLetter(letter)
			publisher3.QueueLetter(letter)
			publisher4.QueueLetter(letter)
		}
	}()

	successCount := 0
	failureCount := 0
	timer := time.NewTimer(1 * time.Minute)

	var notification *models.Notification
ListeningForNotificationsLoop:
	for {
		select {
		case <-timer.C:
			break ListeningForNotificationsLoop
		case notification = <-publisher1.Notifications():
		case notification = <-publisher2.Notifications():
		case notification = <-publisher3.Notifications():
		case notification = <-publisher4.Notifications():
		default:
			time.Sleep(1 * time.Millisecond)
			break
		}

		if notification != nil {
			if notification.Success {
				successCount++
			} else {
				failureCount++
			}

			notification = nil
		}

		if successCount+failureCount == publisherMultiple*messageCount {
			break ListeningForNotificationsLoop
		}
	}

	elapsed = time.Since(timeStart)

	assert.Equal(t, publisherMultiple*messageCount, successCount+failureCount)
	fmt.Printf("All Messages Accounted For: %d\r\n", successCount)
	fmt.Printf("Success Count: %d\r\n", successCount)
	fmt.Printf("Failure Count: %d\r\n", failureCount)
	fmt.Printf("Time Elapsed: %s\r\n", elapsed)
	fmt.Printf("Rate: %f msg/s\r\n", float64(publisherMultiple*messageCount)/elapsed.Seconds())

	// Purge all queues.
	purgeAllPublisherTestQueues("PubTQ", ChannelPool)

	// Shut down everything.
	publisher1.StopAutoPublish()
	publisher2.StopAutoPublish()
	publisher3.StopAutoPublish()
	publisher4.StopAutoPublish()
	channelPool1.Shutdown()
	channelPool2.Shutdown()
	channelPool3.Shutdown()
	channelPool4.Shutdown()
}

func purgeAllPublisherTestQueues(queuePrefix string, channelPool *pools.ChannelPool) {
	topologer, err := topology.NewTopologer(channelPool)
	if err == nil {
		for i := 0; i < 10; i++ {
			if _, err := topologer.PurgeQueue(fmt.Sprintf("%s-%d", queuePrefix, i), false); err != nil {
				fmt.Print(err)
			}
		}
	}
}

func TestCreatePublisherAndPublishWithConfirmation(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	channelPool, err := pools.NewChannelPool(Seasoning.PoolConfig, nil, true)
	assert.NoError(t, err)

	publisher, err := publisher.NewPublisher(Seasoning, channelPool, nil)
	assert.NoError(t, err)
	assert.NotNil(t, publisher)

	letterID := uint64(1)
	body := "\xFF\xFF\x89\xFF\xFF"
	envelope := &models.Envelope{
		Exchange:     "",
		RoutingKey:   "ConfirmationTestQueue",
		ContentType:  "plain/text",
		Mandatory:    false,
		Immediate:    false,
		DeliveryMode: 2,
	}

	letter := &models.Letter{
		LetterID:   letterID,
		RetryCount: uint32(3),
		Body:       []byte(body),
		Envelope:   envelope,
	}

	publisher.PublishWithConfirmation(letter)
	channelPool.Shutdown()

	// Assert on all Notifications
AssertLoop:
	for {
		select {
		case notification := <-publisher.Notifications():
			assert.True(t, notification.Success)
			assert.Equal(t, letterID, notification.LetterID)
			assert.NoError(t, notification.Error)
			break AssertLoop
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	time.Sleep(time.Millisecond * 100)
}

func TestCreatePublisherAndPublishManyWithConfirmation(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	publisher, err := publisher.NewPublisher(Seasoning, ChannelPool, nil)
	assert.NoError(t, err)
	assert.NotNil(t, publisher)

	letterID := uint64(1)
	body := "\xFF\xFF\x89\xFF\xFF"
	envelope := &models.Envelope{
		Exchange:     "",
		RoutingKey:   "ConfirmationTestQueue",
		ContentType:  "plain/text",
		Mandatory:    false,
		Immediate:    false,
		DeliveryMode: 2,
	}

	letter := &models.Letter{
		LetterID:   letterID,
		RetryCount: uint32(3),
		Body:       []byte(body),
		Envelope:   envelope,
	}

	for i := 0; i < 1000; i++ {
		publisher.PublishWithConfirmation(letter)
	}

	ChannelPool.Shutdown()

	// Assert on all Notifications
AssertLoop:
	for {
		select {
		case notification := <-publisher.Notifications():
			assert.True(t, notification.Success)
			assert.Equal(t, letterID, notification.LetterID)
			assert.NoError(t, notification.Error)
			break AssertLoop
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func BenchmarkCreatePublisherAndPublishManyWithConfirmation(b *testing.B) {

	publisher, _ := publisher.NewPublisher(Seasoning, ChannelPool, nil)

	letterID := uint64(1)
	body := "\xFF\xFF\x89\xFF\xFF"
	envelope := &models.Envelope{
		Exchange:     "",
		RoutingKey:   "ConfirmationTestQueue",
		ContentType:  "plain/text",
		Mandatory:    false,
		Immediate:    false,
		DeliveryMode: 2,
	}

	letter := &models.Letter{
		LetterID:   letterID,
		RetryCount: uint32(3),
		Body:       []byte(body),
		Envelope:   envelope,
	}

	for i := 0; i < 1000; i++ {
		publisher.PublishWithConfirmation(letter)
	}
}

func TestCreatePublisherAndParallelPublishManyWithConfirmation(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	publisher, err := publisher.NewPublisher(Seasoning, ChannelPool, nil)
	assert.NoError(t, err)
	assert.NotNil(t, publisher)

	letterID := uint64(1)
	body := "\xFF\xFF\x89\xFF\xFF"
	envelope := &models.Envelope{
		Exchange:     "",
		RoutingKey:   "ConfirmationTestQueue",
		ContentType:  "plain/text",
		Mandatory:    false,
		Immediate:    false,
		DeliveryMode: 2,
	}

	letter := &models.Letter{
		LetterID:   letterID,
		RetryCount: uint32(3),
		Body:       []byte(body),
		Envelope:   envelope,
	}

	wg := &sync.WaitGroup{}
	for i := 0; i < 100000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publisher.PublishWithConfirmation(letter)
		}()
	}
	wg.Wait()

	done := make(chan struct{}, 1)

	go func() {
	NotificationProcessLoop:
		for {
			select {
			case <-publisher.Notifications():

			default:
				done <- struct{}{}
				break NotificationProcessLoop
			}
		}

	}()
	<-done

	ChannelPool.Shutdown()
}

func BenchmarkCreatePublisherAndParallelPublishManyWithConfirmation(b *testing.B) {

	publisher, _ := publisher.NewPublisher(Seasoning, ChannelPool, nil)

	letterID := uint64(1)
	body := "\xFF\xFF\x89\xFF\xFF"
	envelope := &models.Envelope{
		Exchange:     "",
		RoutingKey:   "ConfirmationTestQueue",
		ContentType:  "plain/text",
		Mandatory:    false,
		Immediate:    false,
		DeliveryMode: 2,
	}

	letter := &models.Letter{
		LetterID:   letterID,
		RetryCount: uint32(3),
		Body:       []byte(body),
		Envelope:   envelope,
	}

	wg := &sync.WaitGroup{}
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publisher.PublishWithConfirmation(letter)
		}()
	}
	wg.Wait()

	done := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-publisher.Notifications():

			default:
				done <- struct{}{}
				break
			}
		}

	}()
	<-done
}

func TestCreatePublisherAndParallelPublishWithAutoAckFalse(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	publisher, err := publisher.NewPublisher(Seasoning, ChannelPool, nil)
	assert.NoError(t, err)
	assert.NotNil(t, publisher)

	letterID := uint64(1)
	body := "\xFF\xFF\x89\xFF\xFF"
	envelope := &models.Envelope{
		Exchange:     "",
		RoutingKey:   "ConfirmationTestQueue",
		ContentType:  "plain/text",
		Mandatory:    false,
		Immediate:    false,
		DeliveryMode: 2,
	}

	letter := &models.Letter{
		LetterID:   letterID,
		RetryCount: uint32(3),
		Body:       []byte(body),
		Envelope:   envelope,
	}

	wg := &sync.WaitGroup{}
	for i := 0; i < 100000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publisher.Publish(letter)
		}()
	}
	wg.Wait()

	done := make(chan struct{}, 1)

	go func() {
	NotificationProcessLoop:
		for {
			select {
			case notification := <-publisher.Notifications():
				if !notification.Success && notification.FailedLetter != nil {
					t.Logf("LetterID: %d failed to publish, retrying...", notification.LetterID)
					publisher.Publish(notification.FailedLetter)
				}
			default:
				done <- struct{}{}
				break NotificationProcessLoop
			}
		}

	}()
	<-done

	ChannelPool.Shutdown()
}
