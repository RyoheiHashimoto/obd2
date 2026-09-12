#!/usr/bin/env python3
"""An ISO-TP peer built on python-can and can-isotp.

The Go tests run it to check this module's ISO-TP implementation against an
independent one:

    isotp_peer.py recv TXID RXID [--blocksize N] [--stmin MS]
        prints "ready", then the received message in hex
    isotp_peer.py send TXID RXID < HEX
        sends the message given in hex on stdin

TXID is the CAN identifier the peer sends on (its flow control frames when
receiving), RXID the one it listens to.
"""

import argparse
import sys

import can
import isotp


def make_stack(bus, txid, rxid, blocksize=0, stmin=0, errors=None):
    address = isotp.Address(isotp.AddressingMode.Normal_11bits, txid=txid, rxid=rxid)
    params = {
        "blocksize": blocksize,
        "stmin": stmin,
        "tx_padding": 0x00,
        "blocking_send": True,
    }
    handler = errors.append if errors is not None else None
    return isotp.CanStack(bus, address=address, params=params, error_handler=handler)


def recv(bus, txid, rxid, blocksize, stmin, out):
    errors = []
    stack = make_stack(bus, txid, rxid, blocksize, stmin, errors)
    stack.start()
    try:
        print("ready", file=out, flush=True)
        msg = stack.recv(block=True, timeout=10)
    finally:
        stack.stop()
    if errors:
        raise SystemExit(f"isotp errors: {errors}")
    if msg is None:
        raise SystemExit("no message received")
    print(bytes(msg).hex().upper(), file=out, flush=True)


def send(bus, txid, rxid, data):
    errors = []
    stack = make_stack(bus, txid, rxid, errors=errors)
    stack.start()
    try:
        stack.send(data, send_timeout=10)
    finally:
        stack.stop()
    if errors:
        raise SystemExit(f"isotp errors: {errors}")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("mode", choices=["send", "recv"])
    p.add_argument("txid", type=lambda s: int(s, 16))
    p.add_argument("rxid", type=lambda s: int(s, 16))
    p.add_argument("--blocksize", type=int, default=0)
    p.add_argument("--stmin", type=int, default=0, help="milliseconds")
    p.add_argument("--channel", default="vcan0")
    a = p.parse_args()
    with can.Bus(interface="socketcan", channel=a.channel) as bus:
        if a.mode == "recv":
            recv(bus, a.txid, a.rxid, a.blocksize, a.stmin, sys.stdout)
        else:
            send(bus, a.txid, a.rxid, bytes.fromhex(sys.stdin.read()))


if __name__ == "__main__":
    main()
