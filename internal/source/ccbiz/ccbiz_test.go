package ccbiz

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jasonharker/city-pulse/internal/source"
)

// A real row (OBJECTID 2), as the arcgis client hands it over: OID removed, point added.
const row = `{"USER_newCredentialIdnt":0,"USER_DbaAddressIdnt":null,"USER_addressIdnt":null,
"USER_FullCredentialNumber":"1002273.545","USER_MjblIdNum":null,"USER_attentionName":" ",
"USER_LocationName":"Accudata","USER_attentionLine2":null,"USER_unitLine":null,
"USER_AddressLine1":"3486 Happy Ln","USER_City":"Las Vegas","USER_State":"NV","USER_ZipCode":"89120",
"USER_ProvincePostalCode":"","USER_phoneNumber":null,"USER_CredentialDescription":"Miscellaneous Service",
"USER_StatusDescription":"License Revoked","USER_InitialLicenseIssuedDate":"19930727",
"USER_OutOfBusinessDateActual":"20220214","USER_LocationAddress":"Accudata\r\n3486 Happy Ln\r\nLas Vegas, NV 89120",
"USER_ParcelNumber":"17806101019","USER_CommissionerDistrict":"G","USER_LandUse":"RN",
"_lat":36.1001,"_lon":-115.1002}`

func TestParse(t *testing.T) {
	obs, err := (&Source{}).Parse(source.Record{ID: "1002273.545", Payload: json.RawMessage(row)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Kind": "license", "Name": "Accudata", "Address": "3486 Happy Ln", "City": "Las Vegas",
		"Zip": "89120", "Parcel": "17806101019", "BusinessKey": "cc:1002273",
		"Category": "Miscellaneous Service", "Status": "License Revoked",
	}
	got := map[string]string{
		"Kind": obs.Kind, "Name": obs.Name, "Address": obs.Address, "City": obs.City,
		"Zip": obs.Zip, "Parcel": obs.Parcel, "BusinessKey": obs.BusinessKey,
		"Category": obs.Category, "Status": obs.Status,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if obs.StartDate == nil || obs.StartDate.Format(time.DateOnly) != "1993-07-27" {
		t.Errorf("StartDate = %v", obs.StartDate)
	}
	if obs.EndDate == nil || obs.EndDate.Format(time.DateOnly) != "2022-02-14" {
		t.Errorf("EndDate = %v", obs.EndDate)
	}
	if obs.Lat == nil || *obs.Lat != 36.1001 || *obs.Lon != -115.1002 {
		t.Errorf("point = %v %v", obs.Lat, obs.Lon)
	}
}
