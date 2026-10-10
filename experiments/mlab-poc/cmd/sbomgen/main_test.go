package main

import "testing"

func TestClassifyLicense(t *testing.T) {
	apache := "Apache License\nVersion 2.0, January 2004\nhttp://www.apache.org/licenses/\n..."
	mit := "Permission is hereby granted, free of charge, to any person obtaining a copy of this software..."
	bsd3 := "Redistribution and use in source and binary forms, with or without modification,\nare permitted...\n* Neither the name of the copyright holder nor..."
	bsd2 := "Redistribution and use in source and binary forms, with or without modification,\nare permitted..."
	unknown := "This is some custom license text that we cannot classify."

	testCases := []struct {
		name string
		text string
		want string
	}{
		{"apache-2.0", apache, "Apache-2.0"},
		{"mit", mit, "MIT"},
		{"bsd-3-clause", bsd3, "BSD-3-Clause"},
		{"bsd-2-clause", bsd2, "BSD-2-Clause"},
		{"unknown stays empty", unknown, ""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLicense(tc.text); got != tc.want {
				t.Fatalf("classifyLicense = %q, want %q", got, tc.want)
			}
		})
	}
}
