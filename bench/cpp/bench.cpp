/*
    Benchmarks for the C++ serialize library, mirroring the serialize.go benchmark
    suite (bench_test.go) one for one, for the cross language comparison in the
    serialize.go docs/performance.md.

    Download serialize.h (v1.4.3) from github.com/mas-bandwidth/serialize into this
    directory, then build and run:

        curl -O https://raw.githubusercontent.com/mas-bandwidth/serialize/main/serialize.h
        c++ -O3 -DNDEBUG -std=c++17 -o bench bench.cpp && ./bench

    Methodology: each benchmark runs 5 repetitions and reports the best mean ns/op,
    matching the spirit of go test -bench with multiple counts.
*/

#include "serialize.h"

#include <chrono>
#include <stdio.h>
#include <stdlib.h>

template <typename T> static inline void DoNotOptimize( T const & value )
{
    asm volatile( "" : : "r,m"( value ) : "memory" );
}

static double NowSeconds()
{
    using namespace std::chrono;
    return duration<double>( steady_clock::now().time_since_epoch() ).count();
}

template <typename F> static double Bench( F && f, long iterations, int reps = 5 )
{
    double best = 1e300;
    for ( int r = 0; r < reps; r++ )
    {
        double start = NowSeconds();
        f( iterations );
        double elapsed = NowSeconds() - start;
        double nsPerOp = elapsed * 1e9 / (double) iterations;
        if ( nsPerOp < best )
            best = nsPerOp;
    }
    return best;
}

static void Print( const char * name, double nsPerOp, double bytesPerOp )
{
    printf( "%-28s %10.2f ns/op %12.2f MB/s\n", name, nsPerOp, bytesPerOp / nsPerOp * 1000.0 );
}

static volatile uint32_t sink;

// ---------------------------------------------------------------------------
// bitpacker benchmarks: 1024 values of mixed widths from 1 to 32 bits, same
// value and width pattern as the Go suite. the Go bit writer masks values to
// the bit width internally; correct C++ usage must mask at the call site, so
// the mask is included here.
// ---------------------------------------------------------------------------

static uint8_t g_bits_buffer[65536];

static const int NumValues = 1024;

static long TotalBits()
{
    long totalBits = 0;
    for ( int i = 0; i < NumValues; i++ )
        totalBits += i % 32 + 1;
    return totalBits;
}

static void BenchBitWriterWriteBits()
{
    double ns = Bench( []( long n ) {
        for ( long it = 0; it < n; it++ )
        {
            serialize::BitWriter writer( g_bits_buffer, sizeof( g_bits_buffer ) );
            for ( int i = 0; i < NumValues; i++ )
            {
                const int bits = i % 32 + 1;
                const uint32_t value = ( uint32_t( i ) * 2654435761u ) & uint32_t( ( uint64_t( 1 ) << bits ) - 1 );
                writer.WriteBits( value, bits );
            }
            writer.FlushBits();
            DoNotOptimize( g_bits_buffer[0] );
        }
    }, 1000000 );
    Print( "BitWriterWriteBits", ns, TotalBits() / 8.0 );
}

static void BenchBitReaderReadBits()
{
    serialize::BitWriter writer( g_bits_buffer, sizeof( g_bits_buffer ) );
    for ( int i = 0; i < NumValues; i++ )
    {
        const int bits = i % 32 + 1;
        const uint32_t value = ( uint32_t( i ) * 2654435761u ) & uint32_t( ( uint64_t( 1 ) << bits ) - 1 );
        writer.WriteBits( value, bits );
    }
    writer.FlushBits();
    const int64_t bytesWritten = writer.GetBytesWritten();

    double ns = Bench( [bytesWritten]( long n ) {
        uint32_t sum = 0;
        for ( long it = 0; it < n; it++ )
        {
            serialize::BitReader reader( g_bits_buffer, bytesWritten );
            for ( int i = 0; i < NumValues; i++ )
                sum += reader.ReadBits( i % 32 + 1 );
        }
        sink = sum;
    }, 1000000 );
    Print( "BitReaderReadBits", ns, TotalBits() / 8.0 );
}

// ---------------------------------------------------------------------------
// packet benchmarks: the same representative game network packet as the Go
// suite, serialized with a unified templated serialize function.
// ---------------------------------------------------------------------------

struct BenchPacket
{
    uint64_t sequence;
    float position[3];
    float orientation[4];
    int32_t health;
    uint32_t weapon;
    int32_t ammo[8];
    bool firing;
    int32_t events;
    uint32_t eventIDs[16];
    uint8_t payload[64];

    void Init()
    {
        sequence = 0x123456789ABCDEF0ULL;
        position[0] = 102.4f; position[1] = -55.3f; position[2] = 12.75f;
        orientation[0] = 0.1f; orientation[1] = 0.2f; orientation[2] = 0.3f; orientation[3] = 0.9f;
        health = 731;
        weapon = 11;
        for ( int i = 0; i < 8; i++ )
            ammo[i] = i * 13 % 200;
        firing = true;
        events = 9;
        for ( int i = 0; i < 16; i++ )
            eventIDs[i] = uint32_t( i ) * 2654435761u;
        for ( int i = 0; i < (int) sizeof( payload ); i++ )
            payload[i] = uint8_t( i * 47 );
    }

    template <typename Stream> bool Serialize( Stream & stream )
    {
        serialize_uint64( stream, sequence );
        for ( int i = 0; i < 3; i++ )
            serialize_compressed_float( stream, position[i], -1024.0f, 1024.0f, 0.01f );
        for ( int i = 0; i < 4; i++ )
            serialize_compressed_float( stream, orientation[i], -1.0f, 1.0f, 0.0001f );
        serialize_int( stream, health, 0, 1000 );
        serialize_bits( stream, weapon, 4 );
        for ( int i = 0; i < 8; i++ )
            serialize_int( stream, ammo[i], 0, 200 );
        serialize_bool( stream, firing );
        serialize_int( stream, events, 0, 16 );
        for ( int i = 0; i < events; i++ )
            serialize_bits( stream, eventIDs[i], 32 );
        serialize_bytes( stream, payload, sizeof( payload ) );
        return true;
    }
};

static uint8_t g_packet_buffer[1024 + 8];       // + 8: read buffer allocations extend 8 bytes past the data

static BenchPacket g_packet;

static int64_t g_packet_bytes;

static void BenchWriteStreamPacket()
{
    g_packet.Init();

    serialize::WriteStream stream( g_packet_buffer, 1024 );
    g_packet.Serialize( stream );
    stream.Flush();
    g_packet_bytes = stream.GetBytesProcessed();
    if ( g_packet_bytes != 133 )        // must match the Go benchmark packet exactly (wire compatibility)
    {
        printf( "error: expected 133 byte packet, got %lld\n", (long long) g_packet_bytes );
        exit( 1 );
    }

    double ns = Bench( []( long n ) {
        for ( long it = 0; it < n; it++ )
        {
            serialize::WriteStream stream( g_packet_buffer, 1024 );
            g_packet.Serialize( stream );
            stream.Flush();
            DoNotOptimize( g_packet_buffer[0] );
        }
    }, 10000000 );
    Print( "WriteStreamPacket", ns, (double) g_packet_bytes );
}

static void BenchReadStreamPacket()
{
    double ns = Bench( []( long n ) {
        uint32_t sum = 0;
        BenchPacket readPacket;
        for ( long it = 0; it < n; it++ )
        {
            serialize::ReadStream stream( g_packet_buffer, g_packet_bytes );
            readPacket.Serialize( stream );
            DoNotOptimize( readPacket );        // all fields are live, as in the Go benchmark
            sum += (uint32_t) readPacket.health;
        }
        sink = sum;
    }, 10000000 );
    Print( "ReadStreamPacket", ns, (double) g_packet_bytes );
}

static void BenchMeasureStreamPacket()
{
    double ns = Bench( []( long n ) {
        int64_t total = 0;
        for ( long it = 0; it < n; it++ )
        {
            serialize::MeasureStream stream;
            g_packet.Serialize( stream );
            const int64_t bits = stream.GetBitsProcessed();
            DoNotOptimize( bits );
            total += bits;
        }
        DoNotOptimize( total );
    }, 20000000 );

    serialize::MeasureStream stream;
    g_packet.Serialize( stream );
    Print( "MeasureStreamPacket", ns, (double) stream.GetBytesProcessed() );
}

// ---------------------------------------------------------------------------
// bulk byte benchmarks: an MTU sized payload starting unaligned, so the head,
// aligned middle and tail paths are all exercised, same as the Go suite.
// ---------------------------------------------------------------------------

static const int PayloadSize = 1200;

static uint8_t g_payload[PayloadSize];
static uint8_t g_bulk_buffer[2048 + 8];         // + 8: read buffer allocations extend 8 bytes past the data
static uint8_t g_output[PayloadSize];

static void BenchWriteBytes()
{
    for ( int i = 0; i < PayloadSize; i++ )
        g_payload[i] = uint8_t( i * 31 );

    double ns = Bench( []( long n ) {
        for ( long it = 0; it < n; it++ )
        {
            serialize::BitWriter writer( g_bulk_buffer, 2048 );
            writer.WriteBits( 1, 3 );
            writer.WriteAlign();
            writer.WriteBytes( g_payload, PayloadSize );
            writer.FlushBits();
            DoNotOptimize( g_bulk_buffer[0] );
        }
    }, 20000000 );
    Print( "WriteBytes", ns, (double) PayloadSize );
}

static void BenchReadBytes()
{
    serialize::BitWriter writer( g_bulk_buffer, 2048 );
    writer.WriteBits( 1, 3 );
    writer.WriteAlign();
    writer.WriteBytes( g_payload, PayloadSize );
    writer.FlushBits();
    const int64_t bytesWritten = writer.GetBytesWritten();

    double ns = Bench( [bytesWritten]( long n ) {
        uint32_t sum = 0;
        for ( long it = 0; it < n; it++ )
        {
            serialize::BitReader reader( g_bulk_buffer, bytesWritten );
            sum += reader.ReadBits( 3 );
            reader.ReadAlign();
            reader.ReadBytes( g_output, PayloadSize );
            DoNotOptimize( g_output[0] );
        }
        sink = sum + g_output[PayloadSize-1];
    }, 20000000 );
    Print( "ReadBytes", ns, (double) PayloadSize );
}

int main()
{
    printf( "c++ -O3 -DNDEBUG, serialize.h %s\n\n", SERIALIZE_VERSION );

    BenchBitWriterWriteBits();
    BenchBitReaderReadBits();
    BenchWriteStreamPacket();
    BenchReadStreamPacket();
    BenchMeasureStreamPacket();
    BenchWriteBytes();
    BenchReadBytes();

    return 0;
}
