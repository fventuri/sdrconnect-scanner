#!/usr/bin/env python#
# parses the html from the NRSC RDS PI Code Allocations into CVS format
# suitable for 'label' files
#
# Copyright 2024-2025 Franco Venturi
#
# SPDX-License-Identifier: GPL-3.0-or-later
#
# References:
# - https://picodes.nrscstandards.org/fs_pi_codes_allocated.html
# - https://picodes.nrscstandards.org/pi_codes_allocated.html

from html.parser import HTMLParser
import sys

def fix_location(location):
    return " ".join(location.split()).replace('"', '""')

class PICodesHTMLParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.intbody = False
        self.intr = False
        self.intd = False
        self.fields = list()
        self.field = None

    def handle_starttag(self, tag, attrs):
        if tag == 'tbody':
            self.intbody = True
        elif tag == 'tr':
            if self.intbody:
                self.fields = list()
                self.intr = True
        elif tag == 'td':
            if self.intr:
                field = None
                self.intd = True

    def handle_endtag(self, tag):
        if tag == 'tbody':
            self.intbody = False
        elif tag == 'tr':
            if self.intbody:
                self.print_fields()
                self.intr = False
        elif tag == 'td':
            if self.intr:
                self.fields.append(self.field)
                self.intd = False

    def handle_data(self, data):
        if self.intd:
            self.field = data.strip()

    def print_fields(self):
        #print(self.fields)
        print(f'{self.fields[1]},"{self.fields[0]} - {self.fields[2]} - {self.fields[4]}, {self.fields[5]} - {fix_location(self.fields[6])}"')

parser = PICodesHTMLParser()
sys.stdin.reconfigure(encoding='iso-8859-1')
parser.feed(sys.stdin.read())
