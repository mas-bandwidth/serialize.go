/*
    The C++ half of the cross language wire compatibility harness. Its Go twin is
    ../main.go; the cppcompat CI job runs both against each other on every push and
    PR: each side writes its stream to a file, the two files must be byte identical,
    and each side must read the other's file back to the exact values.

    Download serialize.h (v1.4.3) from github.com/mas-bandwidth/serialize into this
    directory, then build and run:

        curl -O https://raw.githubusercontent.com/mas-bandwidth/serialize/v1.4.3/serialize.h
        c++ -O2 -std=c++17 -Wall -o compat compat.cpp
        ./compat write cpp.bin && ./compat read cpp.bin

    Any change to the value sequence must be mirrored in ../main.go, and never changes
    the wire format: see CLAUDE.md invariant 1.
*/

#include "serialize.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <wchar.h>

struct CompatData
{
    uint32_t bits4;
    uint32_t bits11;
    uint32_t bits24;
    uint32_t bits32;
    int32_t intSmall;
    int32_t intFull;
    bool flag;
    float floatValue;
    float compressedFloatValue;
    double doubleValue;
    uint8_t uint8Value;
    uint16_t uint16Value;
    uint32_t uint32Value;
    uint64_t uint64Value;
    int32_t relativeNear;
    int32_t relativeFar;
    uint8_t bytes[7];
    char str[16];
    wchar_t wstr[8];
    uint64_t bits33;
    int64_t int64Full;
    int64_t int64Range;

    void Init()
    {
        memset( this, 0, sizeof( *this ) );
        bits4 = 13;
        bits11 = 1445;
        bits24 = 11259375;
        bits32 = 0xDEADBEEF;
        intSmall = -37;
        intFull = -123456789;
        flag = true;
        floatValue = 3.1415926f;
        compressedFloatValue = 5.0f;            // 5.0 in [0,10] normalizes to exactly 0.5: quantizes identically everywhere
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
        wcscpy( wstr, L"мир" );  // "мир": cyrillic, BMP only
        bits33 = 0x1DEADBEEFULL;
        int64Full = -123456789012345LL;
        int64Range = 4123456789LL;
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
        serialize_bool( stream, flag );
        serialize_float( stream, floatValue );
        serialize_compressed_float( stream, compressedFloatValue, 0.0f, 10.0f, 0.01f );
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
        serialize_wstring( stream, wstr, sizeof( wstr ) / sizeof( wstr[0] ) );
        serialize_bits( stream, bits33, 33 );
        serialize_int64( stream, int64Full, INT64_MIN, INT64_MAX );
        serialize_int64( stream, int64Range, -5000000000LL, +5000000000LL );
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
            && flag == other.flag
            && floatValue == other.floatValue
            && compressedFloatValue == other.compressedFloatValue
            && doubleValue == other.doubleValue
            && uint8Value == other.uint8Value
            && uint16Value == other.uint16Value
            && uint32Value == other.uint32Value
            && uint64Value == other.uint64Value
            && relativeNear == other.relativeNear
            && relativeFar == other.relativeFar
            && memcmp( bytes, other.bytes, sizeof( bytes ) ) == 0
            && strcmp( str, other.str ) == 0
            && wcscmp( wstr, other.wstr ) == 0
            && bits33 == other.bits33
            && int64Full == other.int64Full
            && int64Range == other.int64Range;
    }
};

static uint8_t g_buffer[256 + 8];               // + 8: the C++ bit reader window loads may extend past the data

static int Write( const char * path )
{
    CompatData data;
    data.Init();

    serialize::WriteStream stream( g_buffer, 256 );
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
    const size_t bytes = fread( g_buffer, 1, 256, file );
    fclose( file );
    if ( bytes == 0 || bytes >= 256 )
    {
        fprintf( stderr, "compat cpp read: unexpected packet size %zu\n", bytes );
        return 1;
    }

    serialize::ReadStream stream( g_buffer, (int) bytes );
    CompatData data;
    memset( &data, 0, sizeof( data ) );
    if ( !data.Serialize( stream ) )
    {
        fprintf( stderr, "compat cpp read: serialize failed\n" );
        return 1;
    }

    CompatData expected;
    expected.Init();
    if ( !data.Equals( expected ) )
    {
        fprintf( stderr, "compat cpp read: decoded values do not match\n" );
        return 1;
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
