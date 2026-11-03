package goyclients

import (
	"fmt"
	"time"
)

const (
	formatDateTime1 = "2006-01-02T15:04:05-0700"
	formatDateTime2 = "2006-01-02T15:04:05-07:00" // visit?
	formatDateTime3 = "2006-01-02 15:04:05" // visit?
	formatDate      = "2006-01-02"

	quoteChar byte = 34
)

type APIDateTime time.Time

func (v *APIDateTime) String() string {
	return time.Time(*v).Format(formatDateTime2)
}

func (v *APIDateTime) UnmarshalJSON(data []byte) error {
	str := ""
	if len(data) > 0 && data[0] == quoteChar && byte(data[len(data)-1]) == quoteChar {
		str = string(data[1 : len(data)-1])
	} else {
		str = string(data)
	}

	if str == "" {
		return nil
	}

	t, err := time.Parse(formatDateTime1, str)
	if err != nil {
		t, err = time.Parse(formatDateTime2, str) // second attempt
		if err != nil {
			t, err = time.Parse(formatDateTime3, str) // third attempt
			if err != nil {
				return err
			}
		}
	}
	*v = APIDateTime(t)
	return nil
}

func (v *APIDateTime) MarshalJSON() ([]byte, error) {
	var b []byte
	return fmt.Appendf(b, `"%s"`, time.Time(*v).Format(formatDateTime1)), nil
}

//********************************************************

type APIDate time.Time

func (v *APIDate) String() string {
	return time.Time(*v).Format(formatDate)
}

func (v *APIDate) UnmarshalJSON(data []byte) error {
	str := ""
	if len(data) > 0 && data[0] == quoteChar && byte(data[len(data)-1]) ==  quoteChar {
		str = string(data[1:len(data)-1])
	}else {
		str = string(data)
	}
	
	if str == "" {
		return nil
	}

	t, err := time.Parse(formatDate, str)
	if err != nil {
		return err
	}
	*v = APIDate(t)
	return nil	
}

func (v *APIDate) MarshalJSON() ([]byte, error) {
	var b []byte
	return fmt.Appendf(b, `"%s"`, time.Time(*v).Format(formatDate)), nil
}

