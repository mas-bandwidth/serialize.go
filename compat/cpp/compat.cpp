/*
    The C++ half of the cross language wire compatibility harness. Its Go twin is
    ../main.go; the cppcompat CI job runs both against each other on every push and
    PR: each side writes its stream to a file, the two files must be byte identical,
    and each side must read the other's file back to the exact values.

    Download serialize.h (v2.1.1) from github.com/mas-bandwidth/serialize into this
    directory, then build and run:

        curl -O https://raw.githubusercontent.com/mas-bandwidth/serialize/v2.1.1/serialize.h
        c++ -O2 -std=c++17 -Wall -o compat compat.cpp
        ./compat write cpp.bin && ./compat read cpp.bin

    The pin is v2.1.1, the same release ci.yml downloads, and it never moves
    backwards past the floors underneath it. It must be at least v2.0.0, the first
    release implementing format version 2.0: wstring is gone and serialize_end is
    new, and this harness uses both facts. It must be at least v1.7.0, the C++
    release whose writer forces the intermediate rounding in compressed_float: the
    harness carries a vector chosen to catch a contracted (FMA) quantization, and
    against v1.6.2 the check is circular on arm64 — clang contracts v1.6.2's writer
    at -O2 exactly like a broken Go writer, both halves write the same wrong byte,
    and the gate passes buggy-against-buggy. It must be at least v1.11.0, the first
    release carrying serialize_compressed_float_precomputed, which this harness
    drives. The older requirement still holds too: the degenerate range
    (min == max) must not abort, which every release since v1.6.2 guarantees.
    Build with asserts ON — they are the C++ half of "API misuse panics", and
    stripping them with -DNDEBUG would hide the divergence this harness exists to
    catch, including the precomputed entry point's own constants check.

    The two entry points cross in OPPOSITE DIRECTIONS on purpose, and neither
    crossing proves what the other does. compressedFloatHalf rides the PRECOMPUTED
    entry point in Go against a DERIVE-PER-CALL C++ side; compressedFloatShift
    rides the PRECOMPUTED entry point here against a DERIVE-PER-CALL Go side. So
    each language's precomputed path is held to the other language's derived path,
    which is the mix a migrating codebase actually runs, and a divergence in either
    direction fails. Because the two entry points are wire identical by
    construction, routing a field through the precomputed one changes no byte of
    this harness's output — that unchanged output is the claim, measured.

    The fixed-point and 128-bit integer tail exercises every wide and fixed-point
    entry point across the Go/C++ boundary: serialize_uint128 (four distinct 32-bit
    wire groups), serialize_int128 across all five branches (degenerate min == max,
    1-group <=32 bits, 2-group 33..64 bits, 3-group 65..96 bits, and 4-group 97..128
    bits both bounded and full-range), serialize_fixed over 16/32/64-bit signed and
    unsigned storage across 1-group, 2-group, and degenerate ranges, and
    serialize_fixed over serialize::int128_t storage across 1-group, 2-group,
    3-group, 4-group, and degenerate ranges. Fixed point and 128-bit integers are
    exact, so the reader recovers the written values bit for bit.

    Any change to the value sequence must be mirrored in ../main.go, and never changes
    the wire format: see CLAUDE.md invariant 1.
*/

#include "serialize.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

struct CompatData
{
    uint32_t bits4;
    uint32_t bits11;
    uint32_t bits24;
    uint32_t bits32;
    int32_t intSmall;
    int32_t intFull;
    int32_t degenerate;
    bool flag;
    float floatValue;
    float compressedFloatValue;
    float compressedFloatHalf;
    float compressedFloatShift;
    float clampRejectWitness;
    float clampWideWitness;
    double doubleValue;
    uint8_t uint8Value;
    uint16_t uint16Value;
    uint32_t uint32Value;
    uint64_t uint64Value;
    int32_t relativeNear;
    int32_t relativeFar;
    uint8_t bytes[7];
    char str[16];
    char text[8];
    uint64_t bits33;
    int64_t int64Full;
    int64_t int64Range;
    serialize::uint128_t uint128Value;
    serialize::int128_t int128Degenerate;
    serialize::int128_t int128OneGroup;
    serialize::int128_t int128TwoGroup;
    serialize::int128_t int128ThreeGroup;
    serialize::int128_t int128FourGroup;
    serialize::int128_t int128Full;
    int16_t fixedQ8_8;
    int32_t fixedQ16_16;
    int64_t fixedQ48_16;
    uint32_t fixedQ16_16Unsigned;
    int64_t fixedQ48_16Degenerate;
    serialize::int128_t fixedQ112_16Small;
    serialize::int128_t fixedQ112_16Mid;
    serialize::int128_t fixedQ112_16Degenerate;
    serialize::int128_t fixedQ112_16Wide;
    serialize::int128_t fixedQ64_64Wide;

    void Init()
    {
        memset( this, 0, sizeof( *this ) );
        bits4 = 13;
        bits11 = 1445;
        bits24 = 11259375;
        bits32 = 0xDEADBEEF;
        intSmall = -37;
        intFull = -123456789;
        degenerate = 42;                        // min == max: known from the range alone, zero bits on the wire
        flag = true;
        floatValue = 3.1415926f;
        compressedFloatValue = 5.0f;            // on-quantum anchor: agrees under float32, double and FMA alike, so it cannot discriminate
        compressedFloatHalf = 0.005f;           // half a quantum above min: an FMA rounds once and writes 0 where the format requires 1
        compressedFloatShift = -42.573f;        // off-quantum over a non-zero min: exercises the (value - min) and + min steps
        clampRejectWitness = 8388609.0f;        // witness A: top of [0, 8388609] res 1 -- an unclamped writer emits a code its own reader rejects (schema#109; serialize#94)
        clampWideWitness = 16777215.0f;         // witness B: top of [0, 16777215] res 1 -- an unclamped writer emits a code one bit wider than the field
        doubleValue = 1.0 / 3.0;
        uint8Value = 0x7F;
        uint16Value = 0x1234;
        uint32Value = 0x12345678;
        uint64Value = 0x123456789ABCDEF0ULL;
        relativeNear = 101;                     // difference of 1 from the base: exercises the one bit branch
        relativeFar = 2100;                     // difference of 2000 from the base: exercises the twelve bit bucket
        const uint8_t bytesInit[7] = { 0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0x01 };
        memcpy( bytes, bytesInit, sizeof( bytes ) );
        strcpy( str, "golden" );
        strcpy( text, "\xD0\xBC\xD0\xB8\xD1\x80" );  // "мир": cyrillic, two byte UTF-8 sequences
        bits33 = 0x1DEADBEEFULL;
        int64Full = -123456789012345LL;
        int64Range = 4123456789LL;
        uint128Value = ( serialize::uint128_t( 0x0123456789ABCDEFULL ) << 64 ) | serialize::uint128_t( 0xFEDCBA9876543210ULL );
        int128Degenerate = ( serialize::int128_t( 1 ) << 100 ) + 7;
        int128OneGroup = serialize::int128_t( -37 );
        int128TwoGroup = serialize::int128_t( 4123456789LL );
        int128ThreeGroup = serialize::int128_t( -0x0123456789ABCDEFLL );
        int128FourGroup = -( ( serialize::int128_t( 0x123456789ULL ) << 64 ) + serialize::int128_t( 0x0FEDCBA987654321ULL ) );
        int128Full = ( serialize::int128_t( 0x0123456789ABCDEFULL ) << 64 ) + serialize::int128_t( 0x0FEDCBA987654321ULL );
        fixedQ8_8 = -( 3 * 256 + 64 );
        fixedQ16_16 = 1234 * 65536 + 32768;
        fixedQ48_16 = -( 54321LL * 65536 + 12345 );
        fixedQ16_16Unsigned = 29999U * 65536U + 65535U;
        fixedQ48_16Degenerate = -( 7LL << 16 );
        fixedQ112_16Small = serialize::int128_t( -( 1LL * 65536 + 16384 ) );
        fixedQ112_16Mid = serialize::int128_t( 12345678901LL * 65536 + 32768 );
        fixedQ112_16Degenerate = serialize::int128_t( 9LL << 16 );
        // the golden wire wide fixed values (../main.go): negative with a
        // non-zero fraction over a negative raw minimum, and a 128 bit raw value
        // whose four 32-bit groups are all distinct
        fixedQ112_16Wide = serialize::int128_t( -( 98765432109LL * 65536 + 4321 ) ); // -98765432109.066 in Q112.16: 75 bits, three groups
        fixedQ64_64Wide = ( serialize::int128_t( 0x0123456789ABCDEFULL ) << 64 ) + serialize::int128_t( 0x0FEDCBA987654321ULL ); // Q64.64 over the full unit range: 128 bits, four groups
    }

    void InitSentinel()
    {
        memset( this, 0x5A, sizeof( *this ) );
        strcpy( str, "sentinel" );
        strcpy( text, "sent" );
        const serialize::int128_t sentinel128 = ( serialize::int128_t( 0x5A5A5A5A5A5A5A5AULL ) << 64 ) | serialize::int128_t( 0xDEADDEADDEADDEADULL );
        uint128Value = ( serialize::uint128_t( 0x5A5A5A5A5A5A5A5AULL ) << 64 ) | serialize::uint128_t( 0xDEADDEADDEADDEADULL );
        int128Degenerate = sentinel128;
        int128OneGroup = sentinel128;
        int128TwoGroup = sentinel128;
        int128ThreeGroup = sentinel128;
        int128FourGroup = sentinel128;
        int128Full = sentinel128;
        fixedQ112_16Small = sentinel128;
        fixedQ112_16Mid = sentinel128;
        fixedQ112_16Degenerate = sentinel128;
        fixedQ112_16Wide = sentinel128;
        fixedQ64_64Wide = sentinel128;
    }

    // What a conformant reader recovers from the wire. Differs from Init only in
    // the two off-quantum compressed floats: compression is lossy by construction,
    // the reader returns the nearest quantum, and the exact float32 reconstructions
    // are pinned as hex literals so both language halves demand bit-identical
    // results. An FMA in either reader changes these bits.
    void InitExpected()
    {
        Init();
        compressedFloatHalf = 0x1.47ae16p-7f;   // decode(1):    float32(1/1000) * 10 + 0, two roundings
        compressedFloatShift = -0x1.548f5cp+5f; // decode(5743): float32(5743/20000) * 200 - 100, two roundings
    }

    template <typename Stream> bool Serialize( Stream & stream )
    {
        const int32_t relativeBase = 100;
        serialize_bits( stream, bits4, 4 );
        serialize_bits( stream, bits11, 11 );
        serialize_bits( stream, bits24, 24 );
        serialize_bits( stream, bits32, 32 );
        serialize_int( stream, intSmall, -100, +100 );
        serialize_int( stream, intFull, INT32_MIN, INT32_MAX );
        serialize_int( stream, degenerate, 42, 42 );    // the degenerate range: zero bits, and every field below must stay put
        serialize_bool( stream, flag );
        serialize_float( stream, floatValue );
        serialize_compressed_float( stream, compressedFloatValue, 0.0f, 10.0f, 0.01f );
        serialize_compressed_float( stream, compressedFloatHalf, 0.0f, 10.0f, 0.01f );
        // constants are exactly serialize_compressed_float_params( -100, 100, 0.01 ):
        // max_integer_value 20000, bits 15, delta 200 — verified against that function
        // rather than hand-derived. Anything else is API misuse and trips the asserts.
        serialize_compressed_float_precomputed( stream, compressedFloatShift, 20000, 15, 200.0f, -100.0f );
        // the clamp witnesses ride the derived-per-call entry point on BOTH language
        // halves: the clamp lives in the audited home both entry points share, and
        // writing max makes it load-bearing -- an unclamped writer changes these bytes
        serialize_compressed_float( stream, clampRejectWitness, 0.0f, 8388609.0f, 1.0f );
        serialize_compressed_float( stream, clampWideWitness, 0.0f, 16777215.0f, 1.0f );
        serialize_double( stream, doubleValue );
        serialize_uint8( stream, uint8Value );
        serialize_uint16( stream, uint16Value );
        serialize_uint32( stream, uint32Value );
        serialize_uint64( stream, uint64Value );
        serialize_int_relative( stream, relativeBase, relativeNear );
        serialize_int_relative( stream, relativeBase, relativeFar );
        serialize_align( stream );
        serialize_bytes( stream, bytes, sizeof( bytes ) );
        serialize_string( stream, str, sizeof( str ) );
        serialize_string( stream, text, sizeof( text ) );
        serialize_bits( stream, bits33, 33 );
        serialize_int64( stream, int64Full, INT64_MIN, INT64_MAX );
        serialize_int64( stream, int64Range, -5000000000LL, +5000000000LL );
        // 128-bit unsigned and signed integers across all five group-count branches
        serialize_uint128( stream, uint128Value );
        const serialize::int128_t int128DegenUnit = ( serialize::int128_t( 1 ) << 100 ) + 7;
        serialize_int128( stream, int128Degenerate, int128DegenUnit, int128DegenUnit );
        serialize_int128( stream, int128OneGroup, serialize::int128_t( -100 ), serialize::int128_t( +100 ) );
        serialize_int128( stream, int128TwoGroup, serialize::int128_t( -5000000000LL ), serialize::int128_t( +5000000000LL ) );
        const serialize::int128_t int128Bound70 = serialize::int128_t( 1 ) << 70;
        serialize_int128( stream, int128ThreeGroup, -int128Bound70, int128Bound70 );
        const serialize::int128_t int128Bound100 = serialize::int128_t( 1 ) << 100;
        serialize_int128( stream, int128FourGroup, -int128Bound100, int128Bound100 );
        const serialize::int128_t int128Min = serialize::int128_t( serialize::uint128_t( 1 ) << 127 );
        const serialize::int128_t int128Max = ~int128Min;
        serialize_int128( stream, int128Full, int128Min, int128Max );
        // 64-bit fixed-point in Go paired with 16/32/64-bit signed and unsigned storage in C++
        serialize_fixed( stream, fixedQ8_8, 8, 8, -100, +100 );
        serialize_fixed( stream, fixedQ16_16, 16, 16, -2000, +2000 );
        serialize_fixed( stream, fixedQ48_16, 48, 16, -100000, +100000 );
        serialize_fixed( stream, fixedQ16_16Unsigned, 16, 16, 0, 30000 );
        serialize_fixed( stream, fixedQ48_16Degenerate, 48, 16, -7, -7 );
        // 128-bit fixed-point across 1-group, 2-group, degenerate, 3-group, and 4-group ranges
        serialize_fixed( stream, fixedQ112_16Small, 112, 16, -2LL, +2LL );
        serialize_fixed( stream, fixedQ112_16Mid, 112, 16, -100000000000LL, +100000000000LL );
        serialize_fixed( stream, fixedQ112_16Degenerate, 112, 16, 9LL, 9LL );
        serialize_fixed( stream, fixedQ112_16Wide, 112, 16, -144115188075855872LL, +144115188075855872LL ); // ±2^57 units: 75 bits, three groups
        serialize_fixed( stream, fixedQ64_64Wide, 64, 64, INT64_MIN, INT64_MAX );               // full unit range: 128 bits, four groups
        // the end of the object: nothing on write, and on read the stream must hold
        // exactly the packet, with zero padding in its final byte (STANDARD.md end)
        serialize_end( stream );
        return true;
    }

    bool Equals( const CompatData & other ) const
    {
        return bits4 == other.bits4
            && bits11 == other.bits11
            && bits24 == other.bits24
            && bits32 == other.bits32
            && intSmall == other.intSmall
            && intFull == other.intFull
            && degenerate == other.degenerate
            && flag == other.flag
            && floatValue == other.floatValue
            && compressedFloatValue == other.compressedFloatValue
            && compressedFloatHalf == other.compressedFloatHalf
            && compressedFloatShift == other.compressedFloatShift
            && clampRejectWitness == other.clampRejectWitness
            && clampWideWitness == other.clampWideWitness
            && doubleValue == other.doubleValue
            && uint8Value == other.uint8Value
            && uint16Value == other.uint16Value
            && uint32Value == other.uint32Value
            && uint64Value == other.uint64Value
            && relativeNear == other.relativeNear
            && relativeFar == other.relativeFar
            && memcmp( bytes, other.bytes, sizeof( bytes ) ) == 0
            && strcmp( str, other.str ) == 0
            && strcmp( text, other.text ) == 0
            && bits33 == other.bits33
            && int64Full == other.int64Full
            && int64Range == other.int64Range
            && uint128Value == other.uint128Value
            && int128Degenerate == other.int128Degenerate
            && int128OneGroup == other.int128OneGroup
            && int128TwoGroup == other.int128TwoGroup
            && int128ThreeGroup == other.int128ThreeGroup
            && int128FourGroup == other.int128FourGroup
            && int128Full == other.int128Full
            && fixedQ8_8 == other.fixedQ8_8
            && fixedQ16_16 == other.fixedQ16_16
            && fixedQ48_16 == other.fixedQ48_16
            && fixedQ16_16Unsigned == other.fixedQ16_16Unsigned
            && fixedQ48_16Degenerate == other.fixedQ48_16Degenerate
            && fixedQ112_16Small == other.fixedQ112_16Small
            && fixedQ112_16Mid == other.fixedQ112_16Mid
            && fixedQ112_16Degenerate == other.fixedQ112_16Degenerate
            && fixedQ112_16Wide == other.fixedQ112_16Wide
            && fixedQ64_64Wide == other.fixedQ64_64Wide;
    }
};

static uint8_t g_buffer[512 + 8];               // + 8: the C++ bit reader window loads may extend past the data

template <typename Stream> static bool SerializeTrailingFixed( Stream & stream, serialize::int128_t & value )
{
    serialize_fixed( stream, value, 64, 64, INT64_MIN, INT64_MAX );
    return true;
}

static int Write( const char * path )
{
    CompatData data;
    data.Init();

    memset( g_buffer, 0, sizeof( g_buffer ) );
    serialize::WriteStream stream( g_buffer, 512 );
    if ( !data.Serialize( stream ) )
    {
        fprintf( stderr, "compat cpp write: serialize failed\n" );
        return 1;
    }
    stream.Flush();

    FILE * file = fopen( path, "wb" );
    if ( !file )
    {
        fprintf( stderr, "compat cpp write: could not open %s\n", path );
        return 1;
    }
    const size_t bytes = (size_t) stream.GetBytesProcessed();
    if ( fwrite( g_buffer, 1, bytes, file ) != bytes )
    {
        fprintf( stderr, "compat cpp write: short write to %s\n", path );
        fclose( file );
        return 1;
    }
    fclose( file );

    printf( "compat cpp write ok\n" );
    return 0;
}

static int Read( const char * path )
{
    FILE * file = fopen( path, "rb" );
    if ( !file )
    {
        fprintf( stderr, "compat cpp read: could not open %s\n", path );
        return 1;
    }
    memset( g_buffer, 0, sizeof( g_buffer ) );
    const size_t bytes = fread( g_buffer, 1, 512, file );
    fclose( file );
    if ( bytes == 0 || bytes >= 512 )
    {
        fprintf( stderr, "compat cpp read: unexpected packet size %zu\n", bytes );
        return 1;
    }

    serialize::ReadStream stream( g_buffer, (int) bytes );
    CompatData data;
    data.InitSentinel();
    if ( !data.Serialize( stream ) )
    {
        fprintf( stderr, "compat cpp read: serialize failed\n" );
        return 1;
    }

    CompatData expected;
    expected.InitExpected();
    if ( !data.Equals( expected ) )
    {
        fprintf( stderr, "compat cpp read: decoded values do not match\n" );
        return 1;
    }
    if ( stream.GetBitsProcessed() != (int64_t) bytes * 8 )
    {
        fprintf( stderr, "compat cpp read: bits processed = %lld, want %lld\n", (long long) stream.GetBitsProcessed(), (long long) bytes * 8 );
        return 1;
    }
    if ( stream.GetBytesProcessed() != (int64_t) bytes )
    {
        fprintf( stderr, "compat cpp read: bytes processed = %lld, want %lld\n", (long long) stream.GetBytesProcessed(), (long long) bytes );
        return 1;
    }

    // Re-encode the decoded struct and verify byte-for-byte identity with the input packet.
    uint8_t reencodeBuf[512 + 8];
    memset( reencodeBuf, 0, sizeof( reencodeBuf ) );
    serialize::WriteStream reencodeStream( reencodeBuf, 512 );
    CompatData reencodeData = data;
    if ( !reencodeData.Serialize( reencodeStream ) )
    {
        fprintf( stderr, "compat cpp read: re-encode serialize failed\n" );
        return 1;
    }
    reencodeStream.Flush();
    if ( (size_t) reencodeStream.GetBytesProcessed() != bytes || memcmp( reencodeBuf, g_buffer, bytes ) != 0 )
    {
        fprintf( stderr, "compat cpp read: re-encoded packet bytes do not match input packet\n" );
        return 1;
    }

    // Truncation sweep: every prefix 0..bytes-1 must fail cleanly without mutating
    // trailing destination fields or advancing on subsequent reads after sticky error.
    CompatData sentinel;
    sentinel.InitSentinel();
    for ( size_t truncLen = 0; truncLen < bytes; ++truncLen )
    {
        uint8_t truncBuf[512 + 8];
        memset( truncBuf, 0, sizeof( truncBuf ) );
        memcpy( truncBuf, g_buffer, truncLen );
        serialize::ReadStream truncStream( truncBuf, (int) truncLen );
        CompatData truncData;
        truncData.InitSentinel();
        if ( truncData.Serialize( truncStream ) )
        {
            fprintf( stderr, "compat cpp read: truncated read at %zu/%zu bytes unexpectedly succeeded\n", truncLen, bytes );
            return 1;
        }
        if ( truncLen <= bytes - 16 && truncData.fixedQ64_64Wide != sentinel.fixedQ64_64Wide )
        {
            fprintf( stderr, "compat cpp read: truncated read at %zu/%zu bytes mutated trailing fixedQ64_64Wide\n", truncLen, bytes );
            return 1;
        }
        const int64_t bitsBefore = truncStream.GetBitsProcessed();
        serialize::int128_t afterFail = sentinel.fixedQ64_64Wide;
        if ( SerializeTrailingFixed( truncStream, afterFail ) )
        {
            fprintf( stderr, "compat cpp read: subsequent read after failed truncated stream at %zu bytes unexpectedly succeeded\n", truncLen );
            return 1;
        }
        if ( truncStream.GetBitsProcessed() != bitsBefore )
        {
            fprintf( stderr, "compat cpp read: subsequent read after failed truncated stream at %zu bytes advanced BitsProcessed (%lld -> %lld)\n", truncLen, (long long) bitsBefore, (long long) truncStream.GetBitsProcessed() );
            return 1;
        }
        if ( afterFail != sentinel.fixedQ64_64Wide )
        {
            fprintf( stderr, "compat cpp read: subsequent read after failed truncated stream at %zu bytes mutated destination\n", truncLen );
            return 1;
        }
    }

    // Negative control 1 (high-group corruption):
    // (a) Corrupt bits inside the 4th (highest) 32-bit wire group of fixedQ64_64Wide and
    // verify that decoding succeeds and differs in fixedQ64_64Wide.
    {
        uint8_t corruptHigh[512 + 8];
        memset( corruptHigh, 0, sizeof( corruptHigh ) );
        memcpy( corruptHigh, g_buffer, bytes );
        corruptHigh[bytes - 2] ^= 0xFF;
        serialize::ReadStream corruptHighStream( corruptHigh, (int) bytes );
        CompatData corruptHighData;
        corruptHighData.InitSentinel();
        if ( !corruptHighData.Serialize( corruptHighStream ) )
        {
            fprintf( stderr, "compat cpp read: negative control (high-group corruption on fixedQ64_64Wide) failed stream decode\n" );
            return 1;
        }
        if ( corruptHighData.Equals( expected ) || corruptHighData.fixedQ64_64Wide == expected.fixedQ64_64Wide )
        {
            fprintf( stderr, "compat cpp read: negative control (high-group corruption on fixedQ64_64Wide) unexpectedly matched expected data\n" );
            return 1;
        }
    }
    // (b) Set all-ones in the high 32-bit group of bounded 3-group fixedQ112_16Wide (11-bit
    // 3rd group with 21 bits of headroom) and verify range rejection + untouched trailing field.
    {
        uint8_t corruptRange[512 + 8];
        memset( corruptRange, 0, sizeof( corruptRange ) );
        memcpy( corruptRange, g_buffer, bytes );
        corruptRange[bytes - 17] = 0xFF;
        corruptRange[bytes - 18] = 0xFF;
        serialize::ReadStream corruptRangeStream( corruptRange, (int) bytes );
        CompatData corruptRangeData;
        corruptRangeData.InitSentinel();
        if ( corruptRangeData.Serialize( corruptRangeStream ) )
        {
            fprintf( stderr, "compat cpp read: negative control (out-of-range high group on fixedQ112_16Wide) unexpectedly succeeded\n" );
            return 1;
        }
        if ( corruptRangeData.fixedQ64_64Wide != sentinel.fixedQ64_64Wide )
        {
            fprintf( stderr, "compat cpp read: negative control (out-of-range high group on fixedQ112_16Wide) mutated trailing fixedQ64_64Wide\n" );
            return 1;
        }
    }

    // Negative control 2 (group-swap on 128-bit field): swap 4 bytes from the low 64-bit half
    // with 4 bytes from the high 64-bit half of fixedQ64_64Wide and verify mismatch.
    {
        uint8_t swapped[512 + 8];
        memset( swapped, 0, sizeof( swapped ) );
        memcpy( swapped, g_buffer, bytes );
        for ( size_t i = 0; i < 4; ++i )
        {
            const size_t loIdx = bytes - 14 + i;
            const size_t hiIdx = bytes - 6 + i;
            const uint8_t tmp = swapped[loIdx];
            swapped[loIdx] = swapped[hiIdx];
            swapped[hiIdx] = tmp;
        }
        serialize::ReadStream swappedStream( swapped, (int) bytes );
        CompatData swappedData;
        swappedData.InitSentinel();
        if ( !swappedData.Serialize( swappedStream ) )
        {
            fprintf( stderr, "compat cpp read: negative control (group swap on fixedQ64_64Wide) failed stream decode\n" );
            return 1;
        }
        if ( swappedData.Equals( expected ) || swappedData.fixedQ64_64Wide == expected.fixedQ64_64Wide )
        {
            fprintf( stderr, "compat cpp read: negative control (group swap on fixedQ64_64Wide) unexpectedly matched expected data\n" );
            return 1;
        }
    }

    printf( "compat cpp read ok\n" );
    return 0;
}

int main( int argc, char ** argv )
{
    if ( argc != 3 )
    {
        fprintf( stderr, "usage: compat-cpp write|read <file>\n" );
        return 2;
    }
    if ( strcmp( argv[1], "write" ) == 0 )
        return Write( argv[2] );
    if ( strcmp( argv[1], "read" ) == 0 )
        return Read( argv[2] );
    fprintf( stderr, "usage: compat-cpp write|read <file>\n" );
    return 2;
}
