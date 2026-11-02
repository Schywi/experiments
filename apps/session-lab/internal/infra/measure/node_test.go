package measure

import "testing"

func TestParsePressureFullAvg10(t *testing.T) {
	body := "some avg10=1.00 avg60=2.00 avg300=0.50 total=123456\n" +
		"full avg10=27.34 avg60=1.00 avg300=0.25 total=65432\n"
	got, err := parsePressureFullAvg10(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != 27.34 {
		t.Fatalf("full avg10 = %v, want 27.34", got)
	}

	if _, err := parsePressureFullAvg10("some avg10=1.00 total=1\n"); err == nil {
		t.Fatalf("missing full line did not error")
	}
}

func TestParseCPUStat(t *testing.T) {
	// total = 10+0+5+50+5+0+0+0 = 70, idle = idle+iowait = 50+5 = 55.
	body := "cpu  10 0 5 50 5 0 0 0 0 0\ncpu0 1 0 0 1 0 0 0 0 0 0\n"
	total, idle, err := parseCPUStat(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if total != 70 || idle != 55 {
		t.Fatalf("total=%d idle=%d, want 70 and 55", total, idle)
	}
}
